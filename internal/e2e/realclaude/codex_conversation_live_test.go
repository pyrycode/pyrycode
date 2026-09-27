//go:build e2e_realclaude

package realclaude

// TestCodexConversationLive is the #2660 deliverable: a real Codex conversation
// driven through the whole daemon the way a multi-agent app drives it. A client
// handshakes over the Noise v2 wire advertising multi_agent, creates a
// conversation with agent codex, sets its model and effort through
// set_session_settings, streams one plain turn, answers one clarifying question,
// and then answers a file-write permission modal twice: declined, the file stays
// absent; accepted, it appears.
//
// The two Codex tests before it drive the runner directly (TestCodexApprovalLive
// in cmd/pyry) or the raw app-server (TestCaptureLive in internal/codexsup).
// This one goes through create_conversation, the pool, the Codex runner factory
// and the relay drain, and back to the client.
//
// It needs PYRY_CODEX_CAPTURE_BIN (Codex codexMinVersionForTest or later) and
// PYRY_CODEX_CAPTURE_HOME (a dedicated Codex home, signed in once with CODEX_HOME
// pointed at it), plus the Claude credentials every daemon test in this package
// needs, because the daemon still supervises a Claude bootstrap session. It skips
// where any of them is missing. It shares that one sign-in with the other two
// Codex tests: do not run them concurrently, since renewal keys are single use and
// two runs log each other out.
//
// The daemon's Codex home is <HOME>/.pyry/test/codex-home under the temp HOME the
// harness mints. The test links that path to PYRY_CODEX_CAPTURE_HOME instead of
// copying the sign-in, and never reads a file in it. The daemon's
// prepareCodexHome rewrites config.toml there on every session start, as it does
// for TestCodexApprovalLive.
//
// permission_mode is deliberately left unset, per the ticket. Read the plan
// (docs/specs/architecture/2660-codex-live-daemon-turn.md) before changing that:
// the pool stores an unset mode as "default", which codexTurnOverrides maps to a
// workspace-write sandbox, so the decline turn may fail with no modal. That
// failure is the finding the ticket asks to be reported, not a flake to be
// fixed by picking another mode here.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// codexMinVersionForTest is cmd/pyry's codexMinVersion, transcribed because
// package main cannot be imported. Below it the daemon refuses the session.
const codexMinVersionForTest = "0.156.1"

// The model and effort the conversation is set to before its first turn. The
// explicit version, because resolveCodexModel sends a family unchanged while the
// daemon holds no Codex model list, and a session with no model would run on the
// account default.
const (
	codexLiveModel         = "gpt-6-luna"
	codexLiveFallbackModel = "gpt-6-sol"
	codexLiveEffort        = "low"
)

// codexTurnBudget bounds one Codex turn from send to turn_end, modal answers
// included. It matches TestCodexApprovalLive's per-turn wait.
const codexTurnBudget = 3 * time.Minute

// codexSettingsBudget bounds a settings request's reply. The session has no
// running Codex yet, so the reply is a registry read or write.
const codexSettingsBudget = 30 * time.Second

func TestCodexConversationLive(t *testing.T) {
	codexBin, captureHome := requireCodexCapture(t)
	h := startCodexConversationHarness(t, codexBin, captureHome)
	nonce := time.Now().UnixNano()

	// AC 1: the create names codex, and the reply names it back.
	agent := protocol.AgentCodex
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:      2,
		Type:    protocol.TypeCreateConversation,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.CreateConversationPayload{Agent: &agent}),
	})
	createdEnv := drainForReply(t, h.phone, h.initRecv, protocol.TypeConversationCreated, 2, 60*time.Second)
	var created protocol.ConversationCreatedPayload
	if err := json.Unmarshal(createdEnv.Payload, &created); err != nil {
		t.Fatalf("decode conversation_created: %v", err)
	}
	if created.ID == "" {
		t.Fatal("conversation_created carried an empty id")
	}
	if created.Agent != protocol.AgentCodex {
		t.Fatalf("conversation_created Agent = %q, want %q", created.Agent, protocol.AgentCodex)
	}
	convID := created.ID

	// AC 2, settings half: learn the session the way an app does, set model and
	// effort, then read them back. The read-back is the deterministic guard that
	// no turn below runs on the account's default model.
	sessionID := requestSessionSettings(t, h, convID, 3).SessionID
	if sessionID == "" {
		t.Fatal("session_settings carried an empty session_id")
	}
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:   4,
		Type: protocol.TypeSetSessionSettings,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SetSessionSettingsPayload{
			SessionID: sessionID,
			Model:     ptr(codexLiveModel),
			Effort:    ptr(codexLiveEffort),
		}),
	})
	drainForReply(t, h.phone, h.initRecv, protocol.TypeSessionSettingsUpdated, 4, codexSettingsBudget)
	if got := requestSessionSettings(t, h, convID, 5); got.Model != codexLiveModel || got.Effort != codexLiveEffort {
		t.Fatalf("session_settings after the change = model %q effort %q, want %q %q; not starting a turn on another model",
			got.Model, got.Effort, codexLiveModel, codexLiveEffort)
	}

	// AC 2, turn half: a plain turn streams a non-empty delta and ends. Codex's
	// words are not asserted.
	reqID := uint64(6)
	plain := runCodexTurn(t, h, convID, reqID,
		fmt.Sprintf("Reply with a single short word. run=%d", nonce),
		turnevent.PermissionOptionKindRejectOnce)
	if !plain.sawDelta {
		t.Fatal("the plain Codex turn ended without a non-empty assistant_delta")
	}

	// #2671: Luna/low gets the first chance to call request_user_input. If it
	// completes without asking, record that result in the live-gate log, switch
	// this session to Sol/low, and repeat the same round-trip proof.
	question := runCodexQuestionTurn(t, h, convID, plain.nextReqID, codexLiveModel,
		codexQuestionPrompt(nonce))
	if !question.asked {
		fmt.Fprintln(os.Stderr, "#2671 Codex question probe: GPT-6 Luna at low effort did not call request_user_input; retrying with GPT-6 Sol at low effort")
		next := setCodexLiveModel(t, h, sessionID, convID, question.nextReqID, codexLiveFallbackModel)
		question = runCodexQuestionTurn(t, h, convID, next, codexLiveFallbackModel,
			codexQuestionPrompt(nonce+1))
		if !question.asked {
			t.Fatal("GPT-6 Sol at low effort also completed without calling request_user_input")
		}
		fmt.Fprintln(os.Stderr, "#2671 Codex question probe: GPT-6 Sol at low effort completed the request_user_input answer round trip")
	} else {
		fmt.Fprintln(os.Stderr, "#2671 Codex question probe: GPT-6 Luna at low effort completed the request_user_input answer round trip")
	}

	// AC 3, declined: the write asks, the answer is no, the file stays absent.
	declined := fmt.Sprintf("declined-%d.txt", nonce)
	decline := runCodexTurn(t, h, convID, question.nextReqID, touchPrompt(declined),
		turnevent.PermissionOptionKindRejectOnce)
	_, statErr := os.Stat(filepath.Join(h.workdir, declined))
	if decline.modals == 0 {
		t.Fatalf("the write turn ended with no permission modal (file present: %v). With permission_mode unset the pool stores %q, "+
			"which codexTurnOverrides maps to a workspace-write sandbox, so Codex may write without asking; report this on #2660 rather than changing the mode here",
			statErr == nil, "default")
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("%s after a declined modal: stat err = %v, want it absent", declined, statErr)
	}

	// AC 3, accepted: the write asks, the answer is yes, the file appears.
	accepted := fmt.Sprintf("accepted-%d.txt", nonce)
	accept := runCodexTurn(t, h, convID, decline.nextReqID, touchPrompt(accepted),
		turnevent.PermissionOptionKindAllowOnce)
	if accept.modals == 0 {
		t.Fatal("the accepted write turn ended with no permission modal")
	}
	if _, err := os.Stat(filepath.Join(h.workdir, accepted)); err != nil {
		t.Fatalf("%s after an accepted modal: %v", accepted, err)
	}
}

func codexQuestionPrompt(nonce int64) string {
	return fmt.Sprintf("Before answering, use request_user_input to ask exactly one single-select question about which in-memory cache eviction policy I prefer. Offer exactly two options with descriptions and allow a free-text alternative. After I answer, reply with only the option or free-text value I selected. Do not choose for me and do not use another tool. run=%d", nonce)
}

// setCodexLiveModel changes the running conversation's model without changing
// its low effort, then reads the settings back before the fallback turn starts.
func setCodexLiveModel(t *testing.T, h *perConvHarness, sessionID, convID string, reqID uint64, model string) uint64 {
	t.Helper()
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeSetSessionSettings,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SetSessionSettingsPayload{
			SessionID: sessionID,
			Model:     ptr(model),
			Effort:    ptr(codexLiveEffort),
		}),
	})
	drainForReply(t, h.phone, h.initRecv, protocol.TypeSessionSettingsUpdated, reqID, codexSettingsBudget)
	reqID++
	got := requestSessionSettings(t, h, convID, reqID)
	if got.Model != model || got.Effort != codexLiveEffort {
		t.Fatalf("fallback session_settings = model %q effort %q, want %q %q", got.Model, got.Effort, model, codexLiveEffort)
	}
	return reqID + 1
}

// touchPrompt asks Codex to create name in its working directory, which is the
// conversation's workspace (the daemon workdir, since the create named no cwd).
func touchPrompt(name string) string {
	return "Use your shell tool to run `touch " + name + "` in the current directory, then reply done."
}

// codexTurn is what runCodexTurn saw between its send and the turn's end.
type codexTurn struct {
	modals    int    // permission modals answered for the conversation
	sawDelta  bool   // a non-empty assistant_delta for the conversation arrived
	nextReqID uint64 // the next unused request id
}

type codexQuestionTurn struct {
	asked     bool
	nextReqID uint64
}

// runCodexQuestionTurn sends a question-producing prompt and drains until the
// turn and, when surfaced, its batch are both terminal. A model that completes
// without calling the tool is reported to the caller so Luna can fall back to
// Sol. Every surfaced question value stays out of the live-gate log.
func runCodexQuestionTurn(t *testing.T, h *perConvHarness, convID string, reqID uint64, model, text string) codexQuestionTurn {
	t.Helper()
	sealSendMessage(t, h.phone, h.initSend, reqID, convID, fmt.Sprintf("m-%d", reqID), text)
	reqID++
	var (
		asked, dismissed, ended bool
		batchID                 string
		selected                string
		otherLabels             []string
		reply                   strings.Builder
	)
	deadline := time.Now().Add(codexTurnBudget)
	for {
		if asked && dismissed && ended {
			requireCodexQuestionChoice(t, reply.String(), selected, otherLabels)
			return codexQuestionTurn{asked: true, nextReqID: reqID}
		}
		env, ok := receiveEnvelope(t, h, time.Until(deadline))
		if !ok {
			t.Fatalf("Codex question turn on %s did not finish within %s (asked=%v dismissed=%v ended=%v)", model, codexTurnBudget, asked, dismissed, ended)
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("error envelope during the Codex question turn: %s", string(env.Payload))
		case protocol.TypeModalShown:
			t.Fatal("Codex raised a permission modal instead of request_user_input for the question prompt")
		case protocol.TypeQuestionShown:
			if asked {
				t.Fatal("Codex surfaced more than one question batch for one prompt")
			}
			var shown protocol.QuestionShownPayload
			if err := json.Unmarshal(env.Payload, &shown); err != nil {
				t.Fatalf("decode question_shown: %v", err)
			}
			if shown.ConversationID != convID || shown.QuestionBatchID == "" || len(shown.Questions) != 1 {
				t.Fatalf("question_shown has conversation %q, batch-present %v, and %d questions; want %q, true, and 1", shown.ConversationID, shown.QuestionBatchID != "", len(shown.Questions), convID)
			}
			q := shown.Questions[0]
			if q.MultiSelect || len(q.Options) < 2 {
				t.Fatalf("Codex question has multi_select=%v and %d options; want false and at least 2", q.MultiSelect, len(q.Options))
			}
			selected = q.Options[len(q.Options)-1].Label
			if selected == "" {
				t.Fatal("Codex question's selected option has an empty label")
			}
			for _, option := range q.Options[:len(q.Options)-1] {
				otherLabels = append(otherLabels, option.Label)
			}
			batchID = shown.QuestionBatchID
			asked = true
			sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
				ID:   reqID,
				Type: protocol.TypeQuestionAnswer,
				TS:   time.Now().UTC(),
				Payload: mustJSON(t, protocol.QuestionAnswerPayload{
					QuestionBatchID: batchID,
					AnswerToken:     fmt.Sprintf("e2e-2671-%d", reqID),
					Answers: []protocol.QuestionAnswerEntry{{
						QuestionIndex: 0,
						Values:        []string{selected},
					}},
				}),
			})
			reqID++
		case protocol.TypeQuestionDismissed:
			var gone protocol.QuestionDismissedPayload
			if err := json.Unmarshal(env.Payload, &gone); err != nil {
				t.Fatalf("decode question_dismissed: %v", err)
			}
			if !asked || gone.QuestionBatchID != batchID || gone.Source != wantQuestionSource || gone.Outcome != wantQuestionOutcome {
				t.Fatalf("question_dismissed did not attribute the surfaced batch to the remote answer")
			}
			dismissed = true
		case protocol.TypeAssistantDelta:
			var delta protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &delta); err != nil {
				t.Fatalf("decode assistant_delta: %v", err)
			}
			if delta.ConversationID == convID {
				reply.WriteString(delta.Text)
			}
		case protocol.TypeTurnEnd:
			var end protocol.TurnEndPayload
			if err := json.Unmarshal(env.Payload, &end); err != nil {
				t.Fatalf("decode turn_end: %v", err)
			}
			if end.ConversationID != convID {
				continue
			}
			if !asked {
				t.Logf("%s at low effort completed the question prompt without calling request_user_input", model)
				return codexQuestionTurn{nextReqID: reqID}
			}
			ended = true
		}
	}
}

func requireCodexQuestionChoice(t *testing.T, reply, selected string, otherLabels []string) {
	t.Helper()
	answer := strings.ToLower(reply)
	selectedAt := strings.Index(answer, strings.ToLower(selected))
	if selectedAt < 0 {
		t.Fatal("Codex's post-question reply did not name the selected option")
	}
	for _, other := range otherLabels {
		if other == "" || strings.EqualFold(other, selected) {
			continue
		}
		if at := strings.Index(answer, strings.ToLower(other)); at >= 0 && at < selectedAt {
			t.Fatal("Codex's post-question reply named an unselected option before the selected option")
		}
	}
}

// runCodexTurn sends text on convID as request reqID and drains the wire in
// receive order until turn_end for convID, answering every permission modal the
// turn raises with answer. Codex may retry a declined action, so each
// modal gets its own answer rather than only the first. A modal_dismissed for a
// modal it answered must carry source remote: a timeout or dropped answer would
// otherwise pass as ours. An error envelope fails the test.
func runCodexTurn(t *testing.T, h *perConvHarness, convID string, reqID uint64, text string, answer turnevent.PermissionOptionKind) codexTurn {
	t.Helper()
	sealSendMessage(t, h.phone, h.initSend, reqID, convID, fmt.Sprintf("m-%d", reqID), text)
	reqID++
	var turn codexTurn
	answered := map[string]bool{}
	deadline := time.Now().Add(codexTurnBudget)
	for {
		env, ok := receiveEnvelope(t, h, time.Until(deadline))
		if !ok {
			t.Fatalf("no turn_end for %q within %s (%d modal(s) answered, delta seen: %v)", convID, codexTurnBudget, turn.modals, turn.sawDelta)
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("error envelope during the Codex turn: %s", string(env.Payload))
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta: %v", err)
			}
			if p.ConversationID == convID && strings.TrimSpace(p.Text) != "" {
				turn.sawDelta = true
			}
		case protocol.TypeModalShown:
			var shown protocol.ModalShownPayload
			if err := json.Unmarshal(env.Payload, &shown); err != nil {
				t.Fatalf("decode modal_shown: %v", err)
			}
			// Not filtered on ConversationID: this daemon holds one conversation,
			// and a Codex modal whose scope came through empty must still be
			// answered rather than left to time out. The scope is logged below.
			if shown.Class != "permission" || shown.ModalID == "" {
				t.Fatalf("modal_shown class %q id %q, want a permission modal with an id", shown.Class, shown.ModalID)
			}
			if !slices.ContainsFunc(shown.Options, func(o protocol.ModalOption) bool { return o.ID == string(answer) }) {
				t.Fatalf("modal_shown options %+v do not offer %q", shown.Options, answer)
			}
			sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
				ID:   reqID,
				Type: protocol.TypeModalAnswer,
				TS:   time.Now().UTC(),
				Payload: mustJSON(t, protocol.ModalAnswerPayload{
					ModalID:     shown.ModalID,
					OptionID:    string(answer),
					AnswerToken: fmt.Sprintf("e2e-2660-%d", reqID),
				}),
			})
			reqID++
			answered[shown.ModalID] = true
			turn.modals++
			t.Logf("answered Codex modal %q (%s, conversation %q) with %s", shown.ModalID, shown.Title, shown.ConversationID, answer)
		case protocol.TypeModalDismissed:
			var dis protocol.ModalDismissedPayload
			if err := json.Unmarshal(env.Payload, &dis); err != nil {
				t.Fatalf("decode modal_dismissed: %v", err)
			}
			if answered[dis.ModalID] && dis.Source != "remote" {
				t.Fatalf("modal %q dismissed by %q, want remote: the answer was not ours", dis.ModalID, dis.Source)
			}
		case protocol.TypeTurnEnd:
			var end protocol.TurnEndPayload
			if err := json.Unmarshal(env.Payload, &end); err != nil {
				t.Fatalf("decode turn_end: %v", err)
			}
			if end.ConversationID == convID {
				turn.nextReqID = reqID
				return turn
			}
		}
	}
}

// receiveEnvelope returns the next noise_msg envelope, decrypting in arrival
// order to keep the receive nonce in step; control frames that are not noise_msg
// are skipped undecrypted. ok is false when timeout passes first.
func receiveEnvelope(t *testing.T, h *perConvHarness, timeout time.Duration) (protocol.Envelope, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return protocol.Envelope{}, false
		}
		raw, err := h.phone.ReceiveBytes(remaining)
		if errors.Is(err, fakephone.ErrReceiveTimeout) {
			return protocol.Envelope{}, false
		}
		if err != nil {
			t.Fatalf("phone receive: %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame: %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data: %v", err)
		}
		plain, err := h.initRecv.Decrypt(cipher)
		if err != nil {
			t.Fatalf("phone decrypt (receive-nonce desync?): %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		return env, true
	}
}

// --- prerequisites -----------------------------------------------------------

// requireCodexCapture returns the Codex binary and the dedicated Codex home the
// operator configured, skipping when either is unset or the binary is older than
// codexMinVersionForTest. It must run before any HOME pinning: it compares the
// home against the operator's real ~/.pyry and fails when it lies inside it, so
// the daemon can never rewrite a production instance's Codex home.
//
// The version is read with CODEX_HOME pointed at an empty directory, so reading
// it cannot touch the sign-in. Nothing in the capture home is ever opened.
func requireCodexCapture(t *testing.T) (bin, home string) {
	t.Helper()
	bin, home = os.Getenv("PYRY_CODEX_CAPTURE_BIN"), os.Getenv("PYRY_CODEX_CAPTURE_HOME")
	var missing []string
	if bin == "" {
		missing = append(missing, "PYRY_CODEX_CAPTURE_BIN")
	}
	if home == "" {
		missing = append(missing, "PYRY_CODEX_CAPTURE_HOME")
	}
	if len(missing) > 0 {
		t.Skipf("set %s to run the live Codex daemon test", strings.Join(missing, " and "))
	}

	cmd := exec.Command(bin, "--version")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+t.TempDir())
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("PYRY_CODEX_CAPTURE_BIN --version: %v", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		t.Fatal("PYRY_CODEX_CAPTURE_BIN --version printed nothing")
	}
	version := fields[len(fields)-1]
	below, ok := codexVersionBelow(version, codexMinVersionForTest)
	if !ok {
		t.Fatalf("cannot parse Codex version %q", version)
	}
	if below {
		t.Skipf("PYRY_CODEX_CAPTURE_BIN is Codex %s, below the required %s", version, codexMinVersionForTest)
	}

	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve the operator's home: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatalf("PYRY_CODEX_CAPTURE_HOME: %v", err)
	}
	if pyryDir, err := filepath.EvalSymlinks(filepath.Join(realHome, ".pyry")); err == nil {
		if rel, err := filepath.Rel(pyryDir, resolved); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("PYRY_CODEX_CAPTURE_HOME resolves inside %s; use a dedicated test home, never an instance's own", pyryDir)
		}
	}
	return bin, resolved
}

// codexVersionBelow reports whether v sorts below floor, reading both as
// MAJOR.MINOR.PATCH with an optional -prerelease or +build suffix; a prerelease
// sorts below its release. The rule is checkCodexVersion's in cmd/pyry. ok is
// false when either does not parse.
func codexVersionBelow(v, floor string) (below, ok bool) {
	have, havePre, ok := parseVersionCore(v)
	if !ok {
		return false, false
	}
	want, _, ok := parseVersionCore(floor)
	if !ok {
		return false, false
	}
	c := slices.Compare(have[:], want[:])
	return c < 0 || (c == 0 && havePre), true
}

func parseVersionCore(v string) (core [3]int, pre, ok bool) {
	v, _, _ = strings.Cut(v, "+")
	v, prerelease, pre := strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) != len(core) || (pre && prerelease == "") {
		return core, false, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return core, false, false
		}
		core[i] = n
	}
	return core, pre, true
}

// --- harness -----------------------------------------------------------------

// startCodexConversationHarness is startPerConversationHarnessSeeded with four
// deltas: the phone is paired with remote permissions (or the modal answer is
// refused at the device gate), the daemon's Codex home is a link to captureHome,
// the daemon is told where Codex is, and the handshake advertises multi_agent,
// without which create_conversation refuses agent codex.
func startCodexConversationHarness(t *testing.T, codexBin, captureHome string) *perConvHarness {
	t.Helper()
	// No t.Parallel: WithWorktreeAuthenticated calls t.Setenv.
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("realclaude: claude not on PATH (the daemon's bootstrap session needs it): %v", err)
	}
	home := WithWorktreeAuthenticated(t) // skips cleanly when no creds

	workdir := filepath.Join(home, "work")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	// The daemon's Codex home, codexHomePath(resolveInstanceDirPath("test")),
	// linked to the dedicated test home. A link rather than a copy: renewal keys
	// are single use, so two copies of one sign-in log each other out.
	instanceDir := filepath.Join(home, ".pyry", "test")
	if err := os.MkdirAll(instanceDir, 0o700); err != nil {
		t.Fatalf("mkdir instance dir: %v", err)
	}
	if err := os.Symlink(captureHome, filepath.Join(instanceDir, "codex-home")); err != nil {
		t.Fatalf("link the daemon's Codex home: %v", err)
	}

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	payload, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: true,
	})
	if err != nil {
		t.Fatalf("paireddevice.Setup: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	seedBootstrapRegistry(t, home, livePerConvBootstrapUUID)

	d := spawnCodexDaemon(t, workdir, claudeBin, codexBin, relayURL)
	t.Cleanup(func() { d.stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	initSend, initRecv := driveHandshake(t, phone, pubKey, payload.Token,
		protocol.CapabilityInteractive, protocol.CapabilityMultiAgent)
	return &perConvHarness{phone: phone, initSend: initSend, initRecv: initRecv, home: home, workdir: workdir, daemon: d}
}

// spawnCodexDaemon forks real pyry like spawnPermissionDaemon, adding
// -pyry-codex before the pass-through. The pass-through carries only the Claude
// bootstrap's model: no --dangerously-skip-permissions, so the daemon's
// operator-bypass provenance stays false and cannot widen the Codex sandbox.
func spawnCodexDaemon(t *testing.T, workdir, claudeBin, codexBin, relayURL string) *bootstrapDaemon {
	t.Helper()
	bin := ensurePyryBuilt(t)
	socket := shortSocketPath(t)
	stderr := &lockedBuffer{}

	cmd := exec.Command(bin,
		"-pyry-socket="+socket,
		"-pyry-name=test",
		"-pyry-claude="+claudeBin,
		"-pyry-codex="+codexBin,
		"-pyry-idle-timeout=0",
		"-pyry-workdir="+workdir,
		"-pyry-relay="+relayURL,
		"--",
		"--model", permissionDaemonModel,
	)
	cmd.Env = append(os.Environ(), "PYRY_ALLOW_INSECURE_RELAY=1", "PYRY_MOBILE_V2=1")
	cmd.Stderr = io.MultiWriter(os.Stderr, stderr)

	if err := cmd.Start(); err != nil {
		t.Fatalf("pyry start: %v", err)
	}
	doneCh := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(doneCh)
	}()

	d := &bootstrapDaemon{socketPath: socket, cmd: cmd, doneCh: doneCh, stderr: stderr}
	if err := d.waitForReady(10 * time.Second); err != nil {
		t.Fatalf("daemon not ready: %v\nstderr:\n%s", err, stderr.String())
	}
	return d
}

// TestCodexVersionBelow pins the skip gate's version comparison offline: it
// must agree with cmd/pyry's checkCodexVersion, or the test would run a Codex
// the daemon refuses, or skip one it accepts.
func TestCodexVersionBelow(t *testing.T) {
	for _, tc := range []struct {
		v         string
		below, ok bool
	}{
		{"0.156.1", false, true},
		{"0.157.0", false, true},
		{"1.0.0", false, true},
		{"0.156.0", true, true},
		{"0.99.9", true, true},
		{"0.156.1-alpha.1", true, true},
		{"0.157.0+build.3", false, true},
		{"0.156", false, false},
		{"v0.157.0", false, false},
		{"0.157.0-", false, false},
	} {
		below, ok := codexVersionBelow(tc.v, codexMinVersionForTest)
		if below != tc.below || ok != tc.ok {
			t.Errorf("codexVersionBelow(%q) = %v, %v; want %v, %v", tc.v, below, ok, tc.below, tc.ok)
		}
	}
}
