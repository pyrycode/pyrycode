//go:build e2e_realclaude

package realclaude

// TestInteractiveStream_NativeReplySuggestionSetThenClear is the #2831 live gate:
// the persistent stream child is spawned with --prompt-suggestions, claude emits
// its native prompt_suggestion after a turn, and the daemon publishes it to an
// interactive client as reply_suggestion. A later accepted send_message must clear
// it with an explicit suggested_reply null at a higher revision.
//
// claude skips suggestions for short conversations, cold caches and turns whose
// next step is not obvious, so the run drives up to suggestTurnBudget small coding
// steps, each with an obvious follow-up, and fails (never skips) when no suggestion
// arrives across all of them. Explicit native enable also permits generation at
// allowed_warning; --prompt-suggestions alone does not override that suppression.
// The suggestion is claude-authored and untrusted: only its length is logged.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

const suggestConvID = "28310000-0000-4000-8000-000000000002"

const (
	suggestTurnBudget = 6
	// suggestWindow bounds the wait for a suggestion after a turn's idle state.
	suggestWindow = 30 * time.Second
	// suggestReadBudget is the single read deadline of the reader goroutine. It
	// outlasts every wait the test makes; the waits time out on its channel.
	suggestReadBudget = 15 * time.Minute
)

// suggestFrame is one envelope the reader goroutine decoded, or the error that
// ended it.
type suggestFrame struct {
	env protocol.Envelope
	err error
}

// startSuggestReader owns every phone read for the run. A fakephone read whose
// timeout fires closes the websocket (coder/websocket ties the conn to the read
// context), so a wait for a suggestion that never comes would leave the next
// turn's send writing to a closed conn. The waits time out on the returned
// channel instead, and reads stay on one goroutine so receive nonces stay ordered.
func startSuggestReader(t *testing.T, h *perConvHarness) <-chan suggestFrame {
	t.Helper()
	frames := make(chan suggestFrame, 64)
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		defer close(frames)
		emit := func(f suggestFrame) bool {
			select {
			case frames <- f:
				return true
			case <-done:
				return false
			}
		}
		for {
			env, err := readSuggestEnvelope(h)
			if errors.Is(err, errSkipFrame) {
				continue
			}
			if !emit(suggestFrame{env: env, err: err}) || err != nil {
				return
			}
		}
	}()
	return frames
}

var errSkipFrame = errors.New("non-noise_msg frame")

// readSuggestEnvelope reads and opens one frame. A non-noise_msg control frame
// (e.g. rekey) carries no envelope and does not advance the receive nonce.
func readSuggestEnvelope(h *perConvHarness) (protocol.Envelope, error) {
	raw, err := h.phone.ReceiveBytes(suggestReadBudget)
	if err != nil {
		return protocol.Envelope{}, fmt.Errorf("phone receive: %w", err)
	}
	var inner protocol.InnerFrameV2
	if err := json.Unmarshal(raw, &inner); err != nil {
		return protocol.Envelope{}, fmt.Errorf("decode inner frame: %w", err)
	}
	if inner.Type != protocol.TypeNoiseMsg {
		return protocol.Envelope{}, errSkipFrame
	}
	cipher, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		return protocol.Envelope{}, fmt.Errorf("decode inner data: %w", err)
	}
	plain, err := h.initRecv.Decrypt(cipher)
	if err != nil {
		return protocol.Envelope{}, fmt.Errorf("phone decrypt (receive-nonce desync?): %w", err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(plain, &env); err != nil {
		return protocol.Envelope{}, fmt.Errorf("decode envelope: %w", err)
	}
	return env, nil
}

// suggestWatch folds every envelope the run reads into the state it asserts on.
type suggestWatch struct {
	sawDelta, idle bool
	setRev         uint64 // latest non-empty suggestion's revision; 0 = none yet
	clearRev       uint64 // revision of a clear above setRev; 0 = none yet
	bounded        bool
}

func (w *suggestWatch) observe(t *testing.T, env protocol.Envelope) {
	t.Helper()
	switch env.Type {
	case protocol.TypeError:
		var ep protocol.ErrorPayload
		if err := json.Unmarshal(env.Payload, &ep); err != nil {
			t.Fatalf("daemon sent an error whose payload did not decode: %v", err)
		}
		t.Fatalf("daemon sent error code %q (retryable=%v)", ep.Code, ep.Retryable)
	case protocol.TypeAssistantDelta:
		var p protocol.AssistantDeltaPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("decode assistant_delta payload: %v", err)
		}
		if p.ConversationID == suggestConvID && strings.TrimSpace(p.Text) != "" {
			w.sawDelta = true
		}
	case protocol.TypeTurnState:
		var st protocol.TurnStatePayload
		if err := json.Unmarshal(env.Payload, &st); err != nil {
			t.Fatalf("decode turn_state payload: %v", err)
		}
		if st.State == "idle" && st.ConversationID == suggestConvID && w.sawDelta {
			w.idle = true
		}
	case protocol.TypeReplySuggestion:
		// Raw suggested_reply so an explicit null is told apart from an omission,
		// which the protocol says does not clear.
		var p struct {
			ConversationID string          `json:"conversation_id"`
			SessionID      string          `json:"session_id"`
			Revision       uint64          `json:"revision"`
			SuggestedReply json.RawMessage `json:"suggested_reply"`
		}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("decode reply_suggestion payload: %v", err)
		}
		if p.ConversationID != suggestConvID {
			return
		}
		if string(p.SuggestedReply) == "null" {
			t.Logf("reply_suggestion clear: revision=%d session_id set=%v", p.Revision, p.SessionID != "")
			if w.setRev != 0 && p.Revision > w.setRev {
				w.clearRev = p.Revision
			}
			return
		}
		var text string
		if err := json.Unmarshal(p.SuggestedReply, &text); err != nil {
			t.Fatalf("reply_suggestion suggested_reply is neither null nor a string: %v", err)
		}
		if w.bounded {
			if !utf8.ValidString(text) || utf8.RuneCountInString(text) > 240 || len(text) > 1024 || strings.TrimSpace(text) == "" {
				t.Fatal("fallback output outside bounds")
			}
			for _, r := range text {
				if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
					t.Fatal("fallback contains control/separator")
				}
			}
		}
		t.Logf("reply_suggestion set: revision=%d length=%d session_id set=%v", p.Revision, len(text), p.SessionID != "")
		if strings.TrimSpace(text) != "" && p.Revision > w.setRev {
			w.setRev = p.Revision
		}
	}
}

// pumpUntil observes envelopes in order until done reports true or timeout passes.
// A timeout leaves the conn open: only the channel wait expires.
func (w *suggestWatch) pumpUntil(t *testing.T, frames <-chan suggestFrame, timeout time.Duration, done func() bool) bool {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for !done() {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatalf("phone reader stopped")
			}
			if f.err != nil {
				t.Fatalf("%v", f.err)
			}
			w.observe(t, f.env)
		case <-timer.C:
			return false
		}
	}
	return true
}

func TestInteractiveStream_NativeReplySuggestionSetThenClear(t *testing.T) {
	evidence := installSuggestCLI(t, true)
	h := startPerConversationHarnessSeeded(t, func(home, workdir string) []string {
		writeStreamInteractiveConfig(t, home)
		seedBoundConversation(t, home, suggestConvID, livePerConvBootstrapUUID, workdir)
		return nil
	})
	nonce := time.Now().UnixNano()
	frames := startSuggestReader(t, h)

	// Coding steps with an obvious next one: claude's suggestion prompt stays
	// silent when the next step is not obvious, so open questions to the user
	// (the first version of this test) drew no suggestion in six turns.
	prompts := []string{
		fmt.Sprintf("Write a Go function that reverses a string. Reply in chat only, do not create files. Just the code, briefly. run=%d", nonce),
		"Now add a unit test for it. Just the code.",
		"Now make it handle unicode correctly. Just the code.",
		"Now add a benchmark for it. Just the code.",
		"Now add a doc comment to the function. Just the code.",
		"Now add an example test for it. Just the code.",
	}

	w := &suggestWatch{}
	var envID uint64 = 1
	turns := 0
	for ; turns < suggestTurnBudget && w.setRev == 0; turns++ {
		envID++
		w.sawDelta, w.idle = false, false
		sealSendMessage(t, h.phone, h.initSend, envID, suggestConvID, fmt.Sprintf("m-%d", turns+1), prompts[turns])
		if !w.pumpUntil(t, frames, perTurnReplyBudget, func() bool { return w.idle }) {
			t.Fatalf("turn %d never reached turn_state{idle} for %q within %s (sawDelta=%v)",
				turns+1, suggestConvID, perTurnReplyBudget, w.sawDelta)
		}
		w.pumpUntil(t, frames, suggestWindow, func() bool { return w.setRev != 0 })
		source := readSuggestSource(t, evidence)
		t.Logf("native source: streams=%d results=%d suggestion_events=%d nonempty_suggestions=%d suggestion_bytes=%d allowed_warning=%d",
			source.Streams, source.Results, source.Events, source.Suggestions, source.Bytes, source.Warnings)
	}
	if w.setRev == 0 {
		t.Fatalf("no non-empty reply_suggestion for %q after %d completed turns (each followed by a %s window)",
			suggestConvID, turns, suggestWindow)
	}
	if source := readSuggestSource(t, evidence); source.Streams != 1 || source.Suggestions == 0 {
		t.Fatal("wire set lacks a nonempty native suggestion from one persistent stream child")
	}
	t.Logf("suggestion set at revision %d after %d turn(s)", w.setRev, turns)

	envID++
	sealSendMessage(t, h.phone, h.initSend, envID, suggestConvID, "m-clear",
		"Sounds good, thank you. Reply with a single short sentence.")
	if !w.pumpUntil(t, frames, perTurnReplyBudget, func() bool { return w.clearRev != 0 }) {
		t.Fatalf("no reply_suggestion with suggested_reply null and revision > %d for %q within %s after the "+
			"accepted send_message", w.setRev, suggestConvID, perTurnReplyBudget)
	}
	t.Logf("suggestion cleared at revision %d (set was %d)", w.clearRev, w.setRev)
}

// installSuggestCLI separates the native and fallback producers using the real
// CLI underneath: native proof refuses every non-stream invocation; fallback
// proof disables prompt suggestions only on the persistent stream child.
func installSuggestCLI(t *testing.T, native bool) string {
	t.Helper()
	real, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude unavailable")
	}
	dir := t.TempDir()
	evidence := filepath.Join(dir, "source.jsonl")
	if err := os.WriteFile(evidence, nil, 0600); err != nil {
		t.Fatal(err)
	}
	mode := 0
	if native {
		mode = 1
	}
	script := fmt.Sprintf(`#!/usr/bin/python3
import json, os, signal, subprocess, sys, time, unicodedata
sys.excepthook = lambda *unused: sys.stderr.write("suggestion observer unavailable\n")
evidence_path = %q
real_cli = %q
def record(fields):
    fd = os.open(evidence_path, os.O_WRONLY | os.O_APPEND)
    try:
        os.write(fd, (json.dumps(fields) + "\n").encode())
    finally:
        os.close(fd)
args = sys.argv[1:]
stream = "--input-format" in args and "stream-json" in args
if %d and not stream:
    sys.exit(1)
if not %d and stream:
    args = [a for a in args if a != "--prompt-suggestions"]
    os.environ["CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION"] = "0"
if %d and stream:
    # Claude's native generator permits allowed_warning only with this explicit
    # enable; the CLI flag alone leaves that independent suppression in place.
    os.environ["CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION"] = "1"
if stream:
    # Keep Claude as the launcher's PID; its stdout observer exits at EOF and
    # belongs to the daemon's existing process group for shutdown.
    read_fd, write_fd = os.pipe()
    if os.fork() == 0:
        os.close(write_fd)
        os.close(0)
        record({"streams": 1})
        with os.fdopen(read_fd, "rb") as source:
            for line in source:
                try:
                    event = json.loads(line)
                    if event.get("type") == "result":
                        record({"results": 1})
                    elif event.get("type") == "prompt_suggestion":
                        record({"events": 1})
                        suggestion = event.get("suggestion")
                        if isinstance(suggestion, str) and suggestion.strip():
                            record({"suggestions": 1, "bytes": len(suggestion.encode("utf-8", "surrogatepass"))})
                    elif event.get("type") == "rate_limit_event":
                        if event.get("rate_limit_info", {}).get("status") == "allowed_warning":
                            record({"warnings": 1})
                except (ValueError, AttributeError, TypeError):
                    pass # Unrecognized output is still forwarded unchanged.
                sys.stdout.buffer.write(line)
                sys.stdout.buffer.flush()
        os._exit(0)
    os.close(read_fd)
    os.dup2(write_fd, 1)
    os.close(write_fd)
    os.execv(real_cli, [real_cli] + args)
# The print-mode parent stays in the production process group, so deadline
# cancellation kills it and the real CLI together. No input or stderr is read.
started = time.monotonic()
record({"calls": 1, "start_ms": time.time_ns() // 1000000, "pid": os.getpid()})
try:
    child = subprocess.Popen([real_cli] + args, stdout=subprocess.PIPE)
except OSError:
    record({"completed": 1, "elapsed_ms": int((time.monotonic() - started) * 1000)})
    sys.exit(1)
record({"progress": "spawn", "pid": os.getpid(), "child_pid": child.pid})
def progress(stage, count):
    record({"progress": stage, "pid": os.getpid(), "child_pid": child.pid,
            "bytes": min(4097, count), "saturated": count >= 4097})
fields = {"utf8_ok": True, "json_ok": False, "result_ok": False, "text_ok": False, "result_bytes": None}
def observe(data):
    try:
        raw = data.decode("utf-8")
        if len(data) <= 4096:
            def reject_constant(value):
                raise ValueError()
            result = json.loads(raw, parse_constant=reject_constant,
                                object_pairs_hook=lambda pairs: ("object", pairs))
            if isinstance(result, tuple):
                pairs = [(k.casefold(), v) for k, v in result[1]]
                # Go reports a type error even if a later duplicate is well typed.
                for k, v in pairs:
                    if ((k in ("type", "result", "subtype") and v is not None and not isinstance(v, str)) or
                            (k == "is_error" and v is not None and not isinstance(v, bool))):
                        raise ValueError()
                # encoding/json matches these struct fields case-insensitively.
                # JSON null leaves Go's existing string/bool struct field unchanged.
                result = {}
                for k, v in pairs:
                    if v is not None:
                        result[k] = v
                if result.get("type") != "result":
                    return
                text = result.get("result")
                subtype = result.get("subtype")
                text = "" if text is None else text
                subtype = "" if subtype is None else subtype
                is_error = result.get("is_error")
                if (isinstance(text, str) and isinstance(subtype, str) and
                        (is_error is None or isinstance(is_error, bool))):
                    fields["utf8_ok"] = True
                    fields["json_ok"] = True
                    fields["result_bytes"] = len(data)
                    fields["result_ok"] = not is_error and subtype in ("", "success")
                    # encoding/json replaces unpaired escaped UTF-16 surrogates.
                    text = text.encode("utf-16", "surrogatepass").decode("utf-16", "replace")
                    # Match strings.TrimSpace, not Python's broader C0 whitespace.
                    text = text.strip("\t\n\v\f\r \u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000")
                    fields["text_ok"] = (0 < len(text) <= 240 and len(text.encode("utf-8")) <= 1024 and
                        not any(unicodedata.category(c) == "Cc" or c in "\u2028\u2029" for c in text))
    except UnicodeError:
        if not fields["json_ok"]:
            fields["utf8_ok"] = False
    except (ValueError, TypeError, AttributeError):
        pass # Only fixed predicates survive; never report exception/output text.
line = bytearray()
oversized = False
size = 0
forwarded = 0
while True:
    chunk = os.read(child.stdout.fileno(), 8192)
    if not chunk:
        break
    first_read = size == 0
    size = min(4097, size + len(chunk))
    if first_read:
        progress("read", size)
    for byte in chunk:
        if byte == 10:
            if not oversized:
                observe(line)
            line.clear()
            oversized = False
        elif not oversized:
            if len(line) == 4096:
                line.clear()
                oversized = True
            else:
                line.append(byte)
    acknowledged = sys.stdout.buffer.write(chunk)
    sys.stdout.buffer.flush()
    first_forward = forwarded == 0
    forwarded = min(4097, forwarded + acknowledged)
    if first_forward and acknowledged > 0:
        progress("forward", forwarded)
if line and not oversized:
    observe(line)
code = child.wait()
fields.update({"completed": 1, "elapsed_ms": int((time.monotonic() - started) * 1000),
          "pid": os.getpid(), "child_pid": child.pid,
          "exit_code": code, "stdout_bytes": size, "output_observed": True})
record(fields)
if code < 0:
    signal.signal(-code, signal.SIG_DFL)
    os.kill(os.getpid(), -code)
sys.exit(code)
`, evidence, real, mode, mode, mode)
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return evidence
}

// suggestSource contains only source metadata. Generated text and credentials
// never enter its evidence file or the test's diagnostic logs.
type suggestSource struct {
	ChildPID                                            int  `json:"child_pid"`
	Started, ReadKnown, ForwardKnown, ProgressAmbiguous bool `json:"-"`
	ReadBytes, ForwardBytes                             int  `json:"-"`

	PID            int   `json:"pid"`
	Streams        int   `json:"streams"`
	Results        int   `json:"results"`
	Events         int   `json:"events"`
	Suggestions    int   `json:"suggestions"`
	Bytes          int   `json:"bytes"`
	Warnings       int   `json:"warnings"`
	Calls          int   `json:"calls"`
	Completed      int   `json:"completed"`
	StartMS        int64 `json:"start_ms"`
	ElapsedMS      int64 `json:"elapsed_ms"`
	ExitCode       *int  `json:"exit_code"`
	ResultBytes    *int  `json:"result_bytes"`
	StdoutBytes    int   `json:"stdout_bytes"`
	OutputObserved bool  `json:"output_observed"`
	UTF8OK         bool  `json:"utf8_ok"`
	JSONOK         bool  `json:"json_ok"`
	ResultOK       bool  `json:"result_ok"`
	TextOK         bool  `json:"text_ok"`
}

func (s suggestSource) stage(wireSet bool) string {
	switch {
	case wireSet:
		return "wire set observed"
	case s.Calls == 0:
		return "no observed invocation (cause unknown)"
	case s.ProgressAmbiguous || s.Calls != 1 || s.Completed > s.Calls:
		return "unknown (ambiguous invocation evidence)"
	case s.Completed == 0:
		return "incomplete invocation (exit/output unknown)"
	case s.ExitCode == nil:
		return "unknown (no observed child exit)"
	case *s.ExitCode != 0:
		return "unsuccessful invocation"
	case !s.OutputObserved:
		return "unknown (no output validation evidence)"
	case s.ResultBytes == nil || *s.ResultBytes < 1 || *s.ResultBytes > 4096 || !s.UTF8OK || !s.JSONOK || !s.ResultOK || !s.TextOK:
		return "unusable output"
	default:
		return "usable output without wire set"
	}
}

// diagnostic uses only fixed labels and scalar metadata, never source values.
// Spawn proves creation; read/flush prefixes prove neither readiness nor daemon receipt.
func (s suggestSource) diagnostic(idle bool, setRev, clearRev uint64) string {
	exit, output := "unknown", "unknown"
	if s.ExitCode != nil {
		exit = fmt.Sprint(*s.ExitCode)
	}
	if s.OutputObserved {
		jsonOK, resultOK, textOK := "unknown", "unknown", "unknown"
		if s.UTF8OK {
			jsonOK = strconv.FormatBool(s.JSONOK)
		}
		if s.UTF8OK && s.JSONOK {
			resultOK, textOK = strconv.FormatBool(s.ResultOK), strconv.FormatBool(s.TextOK)
		}
		output = fmt.Sprintf("bytes=%d utf8=%v json=%s result_success=%s text_valid=%s",
			s.StdoutBytes, s.UTF8OK, jsonOK, resultOK, textOK)
	}
	resultBytes := "unknown"
	if s.OutputObserved && s.JSONOK && s.ResultBytes != nil && *s.ResultBytes > 0 && *s.ResultBytes <= 4096 {
		resultBytes = strconv.Itoa(*s.ResultBytes)
	}
	if s.OutputObserved {
		output += " result_bytes=" + resultBytes
	}
	elapsed, wrapperPID := "unknown", "unknown"
	if s.Completed == 1 {
		elapsed = strconv.FormatInt(s.ElapsedMS, 10)
	} else if s.StartMS > 0 {
		elapsed = strconv.FormatInt(max(0, time.Now().UnixMilli()-s.StartMS), 10)
	}
	if s.PID > 0 {
		wrapperPID = strconv.Itoa(s.PID)
	}
	start, read, forward := "unknown", "unknown", "unknown"
	if s.Started {
		start = "true"
	}
	if s.ReadKnown {
		read = fmt.Sprintf("bytes=%d saturated=%v", s.ReadBytes, s.ReadBytes == 4097)
	}
	if s.ForwardKnown {
		forward = fmt.Sprintf("bytes=%d saturated=%v", s.ForwardBytes, s.ForwardBytes == 4097)
	}
	if s.ProgressAmbiguous {
		start, read, forward = "ambiguous", "ambiguous", "ambiguous"
	}
	childPID := "unknown"
	if s.Started {
		childPID = strconv.Itoa(s.ChildPID)
	}
	progress := fmt.Sprintf(" child_pid=%s spawn=%s read={%s} forward={%s}", childPID, start, read, forward)
	return fmt.Sprintf("fallback source: streams=%d results=%d idle=%v calls=%d completed=%d pid=%s elapsed_ms=%s exit=%s output={%s} set_revision=%d clear_revision=%d stage=%s",
		s.Streams, s.Results, idle, s.Calls, s.Completed, wrapperPID, elapsed, exit, output, setRev, clearRev, s.stage(setRev != 0)) + progress
}

// suggestLifecycle selects complete daemon records by wrapper PID. Only parsed
// scalars are returned; captured stderr and unknown fields are never echoed.
func suggestLifecycle(stderr string, pid int) string {
	keys := []string{"pid", "attempt_ms", "child_ms", "parent_canceled", "parent_deadline", "fallback_canceled", "fallback_deadline", "own_deadline_elapsed", "group_cancel_requested", "wait_completed", "exit_observed", "exit_code", "exit_signal"}
	lines := strings.Split(stderr, "\n")
	for _, line := range lines[:len(lines)-1] {
		fields := make(map[string]string)
		for _, token := range strings.Fields(line) {
			key, value, ok := strings.Cut(token, "=")
			if ok {
				fields[key] = value
			}
		}
		if fields["msg"] != "reply_fallback.lifecycle" || pid <= 0 || fields["pid"] != strconv.Itoa(pid) {
			continue
		}
		var safe []string
		for i, key := range keys {
			value := fields[key]
			if i < 3 || i > 10 {
				n, err := strconv.ParseInt(value, 10, 64)
				if err != nil {
					if value == "unknown" && (key == "exit_code" || key == "exit_signal") && (fields["wait_completed"] != "true" || fields["exit_observed"] != "true") {
						safe = append(safe, key+"=unknown")
						continue
					}
					break
				}
				value = strconv.FormatInt(n, 10)
			} else {
				if value != "true" && value != "false" {
					break
				}
			}
			safe = append(safe, key+"="+value)
		}
		if len(safe) == len(keys) {
			// Presence and applicability gates prevent missing fields becoming zeros
			// or successful predicates. Only normalized scalars leave this parser.
			values := make(map[string]string)
			for _, key := range []string{"output_observed", "wait_ok", "wait_delay", "stdout_cap_exceeded", "stdout_utf8_ok", "stdout_json_ok", "stdout_result_ok", "stdout_text_ok"} {
				values[key] = "unknown"
				if fields["wait_completed"] == "true" {
					if value := fields[key]; value == "true" || value == "false" {
						values[key] = value
					}
				}
			}
			waitKnown := values["wait_ok"] != "unknown" && values["wait_delay"] != "unknown"
			if values["wait_ok"] == "true" && values["wait_delay"] == "true" ||
				(values["wait_ok"] == "true" || values["wait_delay"] == "true") &&
					(fields["exit_observed"] != "true" || fields["exit_code"] != "0" || fields["exit_signal"] != "0") {
				waitKnown = false
			}
			if !waitKnown {
				values["wait_ok"], values["wait_delay"] = "unknown", "unknown"
			}
			values["stdout_bytes"] = "unknown"
			decodeKnown := values["stdout_utf8_ok"] == "false" || values["stdout_json_ok"] != "unknown"
			if values["output_observed"] == "true" && waitKnown && decodeKnown && values["stdout_utf8_ok"] != "unknown" && values["stdout_cap_exceeded"] != "unknown" {
				if n, err := strconv.ParseInt(fields["stdout_bytes"], 10, 64); err == nil && n >= 0 && n <= 4097 && values["stdout_cap_exceeded"] == strconv.FormatBool(n == 4097) {
					// Empty stdout is valid UTF-8 and fails JSON decoding.
					if n != 0 || values["stdout_utf8_ok"] == "true" && values["stdout_json_ok"] == "false" {
						values["stdout_bytes"] = strconv.FormatInt(n, 10)
					}
				}
			}
			if values["stdout_bytes"] == "unknown" {
				for _, key := range []string{"output_observed", "stdout_cap_exceeded", "stdout_utf8_ok", "stdout_json_ok", "stdout_result_ok", "stdout_text_ok"} {
					values[key] = "unknown"
				}
			}
			if values["stdout_utf8_ok"] != "true" {
				values["stdout_json_ok"] = "unknown"
			}
			values["result_bytes"] = "unknown"
			if fields["wait_completed"] == "true" && values["stdout_json_ok"] == "true" {
				n, err := strconv.Atoi(fields["result_bytes"])
				count, _ := strconv.Atoi(values["stdout_bytes"])
				if err == nil && n > 0 && n <= 4096 && n <= count {
					values["result_bytes"] = strconv.Itoa(n)
				} else {
					values["stdout_json_ok"] = "unknown"
				}
			}
			if values["stdout_json_ok"] != "true" {
				values["stdout_result_ok"], values["stdout_text_ok"] = "unknown", "unknown"
			}
			if fields["wait_completed"] != "true" || fields["exit_observed"] != "true" {
				safe[len(safe)-2], safe[len(safe)-1] = "exit_code=unknown", "exit_signal=unknown"
			}
			for _, key := range []string{"output_observed", "wait_ok", "wait_delay", "stdout_bytes", "stdout_cap_exceeded", "stdout_utf8_ok", "stdout_json_ok", "stdout_result_ok", "stdout_text_ok"} {
				safe = append(safe, key+"="+values[key])
			}
			safe = append(safe, "result_bytes="+values["result_bytes"])
			safe = append(safe, suggestProgressFields(fields)...)
			return "daemon lifecycle (cause not inferred): " + strings.Join(safe, " ")
		}
	}
	return "daemon lifecycle: unknown"
}

func (s *suggestSource) invalidateProgress() {
	s.Started, s.ReadKnown, s.ForwardKnown = false, false, false
	s.ChildPID, s.ReadBytes, s.ForwardBytes = 0, 0, 0
	s.ProgressAmbiguous = true
	s.Completed, s.ElapsedMS = 0, 0
	s.OutputObserved, s.ExitCode, s.ResultBytes = false, nil, nil
}

// suggestMetadata requires unique keys; missing and null scalars remain unknown.
func suggestMetadata(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage)
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, false
		}
		name, ok := key.(string)
		if !ok || fields[name] != nil {
			return nil, false
		}
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return nil, false
		}
		fields[name] = value
	}
	_, err = dec.Token()
	return fields, err == nil
}

func readSuggestSource(t *testing.T, path string) suggestSource {
	t.Helper()
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return suggestSource{}
	}
	if err != nil {
		t.Fatal("open native source evidence failed")
	}
	defer f.Close()
	var total suggestSource
	dec := json.NewDecoder(io.LimitReader(f, 64*1024))
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return total // An interrupted final append preserves earlier witnesses.
		} else if err != nil {
			total.invalidateProgress()
			return total
		}
		fields, unique := suggestMetadata(raw)
		var entry suggestSource
		if !unique || json.Unmarshal(raw, &entry) != nil {
			total.invalidateProgress()
			continue
		}
		present := func(names ...string) bool {
			for _, name := range names {
				if fields[name] == nil || string(fields[name]) == "null" {
					return false
				}
			}
			return true
		}
		// Decode exact key names so case-folded aliases cannot supply metadata.
		decode := func(name string, value any) bool {
			return present(name) && json.Unmarshal(fields[name], value) == nil
		}
		if fields["calls"] != nil {
			valid := decode("calls", &entry.Calls) && entry.Calls == 1 &&
				decode("pid", &entry.PID) && entry.PID > 0 &&
				decode("start_ms", &entry.StartMS) && entry.StartMS > 0 &&
				fields["progress"] == nil && fields["completed"] == nil
			if !valid || total.Calls != 0 || total.ProgressAmbiguous {
				total.invalidateProgress()
				continue
			}
			total.Calls, total.PID, total.StartMS = 1, entry.PID, entry.StartMS
			continue
		}
		if fields["progress"] != nil {
			var stage string
			var count int
			var saturated bool
			valid := decode("progress", &stage) && !total.ProgressAmbiguous && total.Calls == 1 && total.Completed == 0 &&
				decode("pid", &entry.PID) && entry.PID == total.PID &&
				decode("child_pid", &entry.ChildPID) && entry.ChildPID > 0 && entry.ChildPID != entry.PID && fields["completed"] == nil
			if stage == "spawn" {
				valid = valid && !total.Started && !total.ReadKnown && !total.ForwardKnown
				if valid {
					total.Started, total.ChildPID = true, entry.ChildPID
				}
			} else {
				valid = valid && total.Started && entry.ChildPID == total.ChildPID && decode("bytes", &count) &&
					count > 0 && count <= 4097 && decode("saturated", &saturated) && saturated == (count == 4097)
				switch stage {
				case "read":
					valid = valid && !total.ReadKnown && !total.ForwardKnown
					if valid {
						total.ReadKnown, total.ReadBytes = true, count
					}
				case "forward":
					valid = valid && total.ReadKnown && !total.ForwardKnown && count <= total.ReadBytes
					if valid {
						total.ForwardKnown, total.ForwardBytes = true, count
					}
				default:
					valid = false
				}
			}
			if !valid {
				total.invalidateProgress()
			}
			continue
		}
		if fields["completed"] != nil {
			valid := !total.ProgressAmbiguous && total.Started && total.Completed == 0 &&
				decode("completed", &entry.Completed) && entry.Completed == 1 &&
				decode("pid", &entry.PID) && entry.PID == total.PID &&
				decode("child_pid", &entry.ChildPID) && entry.ChildPID == total.ChildPID &&
				decode("elapsed_ms", &entry.ElapsedMS) && entry.ElapsedMS >= 0 &&
				decode("exit_code", &entry.ExitCode) && entry.ExitCode != nil && *entry.ExitCode >= -255 && *entry.ExitCode <= 255 &&
				decode("stdout_bytes", &entry.StdoutBytes) && entry.StdoutBytes >= 0 && entry.StdoutBytes <= 4097 &&
				decode("output_observed", &entry.OutputObserved) && entry.OutputObserved &&
				decode("utf8_ok", &entry.UTF8OK) && decode("json_ok", &entry.JSONOK) &&
				decode("result_ok", &entry.ResultOK) && decode("text_ok", &entry.TextOK)
			valid = valid && ((!entry.JSONOK && !entry.ResultOK && !entry.TextOK) ||
				(entry.JSONOK && entry.UTF8OK && entry.StdoutBytes > 0 && decode("result_bytes", &entry.ResultBytes) && entry.ResultBytes != nil && *entry.ResultBytes > 0 && *entry.ResultBytes <= 4096 && *entry.ResultBytes <= entry.StdoutBytes)) &&
				(entry.StdoutBytes != 0 || entry.UTF8OK) &&
				((entry.StdoutBytes == 0 && !total.ReadKnown && !total.ForwardKnown) ||
					(entry.StdoutBytes > 0 && total.ReadKnown && total.ForwardKnown && entry.StdoutBytes >= total.ReadBytes && entry.StdoutBytes >= total.ForwardBytes))
			if !valid {
				total.invalidateProgress()
				continue
			}
			total.Completed, total.ElapsedMS, total.ExitCode, total.StdoutBytes = 1, entry.ElapsedMS, entry.ExitCode, entry.StdoutBytes
			total.OutputObserved, total.UTF8OK, total.JSONOK = true, entry.UTF8OK, entry.JSONOK
			total.ResultOK, total.TextOK, total.ResultBytes = entry.ResultOK, entry.TextOK, entry.ResultBytes
			continue
		}
		// Stream records are independent of the print-mode invocation.
		for _, counter := range []struct {
			name  string
			total *int
		}{{"streams", &total.Streams}, {"results", &total.Results}, {"events", &total.Events},
			{"suggestions", &total.Suggestions}, {"bytes", &total.Bytes}, {"warnings", &total.Warnings}} {
			var n int
			if decode(counter.name, &n) && n > 0 && n <= 1<<30 {
				*counter.total += n
			}
		}
	}
}

func TestInteractiveStream_FallbackReplySuggestionSetThenClear(t *testing.T) {
	evidence := installSuggestCLI(t, false)
	h := startPerConversationHarnessSeeded(t, func(home, workdir string) []string {
		writeStreamInteractiveConfig(t, home)
		seedBoundConversation(t, home, suggestConvID, livePerConvBootstrapUUID, workdir)
		return nil
	})
	frames := startSuggestReader(t, h)
	w := &suggestWatch{bounded: true}
	logSource := func() {
		source := readSuggestSource(t, evidence)
		t.Log(source.diagnostic(w.idle, w.setRev, w.clearRev))
		t.Log(suggestLifecycle(h.daemon.stderr.String(), source.PID))
	}
	defer logSource()
	sealSendMessage(t, h.phone, h.initSend, 2, suggestConvID, "fallback-turn", "Suggest a simple Go testing task I can do next. One sentence, no tools.")
	if !w.pumpUntil(t, frames, perTurnReplyBudget, func() bool { return w.idle }) {
		logSource()
		t.Fatal("exchange did not complete")
	}
	// Includes the two-second native window and ten-second production attempt.
	if !w.pumpUntil(t, frames, 15*time.Second, func() bool { return w.setRev != 0 }) {
		logSource()
		t.Fatal("production Haiku fallback absent after completed exchange")
	}
	logSource()
	sealSendMessage(t, h.phone, h.initSend, 3, suggestConvID, "fallback-clear", "Thank you. Reply briefly.")
	if !w.pumpUntil(t, frames, perTurnReplyBudget, func() bool { return w.clearRev > w.setRev }) {
		logSource()
		t.Fatal("explicit-null clear at higher revision absent")
	}
	logSource()
}

func suggestProgressFields(fields map[string]string) []string {
	var safe []string
	for _, prefix := range []string{"progress_", "cancel_"} {
		event, age, init, source, retries := "unknown", "unknown", "unknown", "unknown", "unknown"
		applicable := prefix == "progress_" || fields["cancel_snapshot_observed"] == "true"
		if applicable {
			switch fields[prefix+"event"] {
			case "none", "init", "api_retry", "result":
				event = fields[prefix+"event"]
			}
			if event != "unknown" {
				if value := fields[prefix+"init"]; value == "true" || value == "false" {
					init = value
				}
				if n, err := strconv.ParseInt(fields[prefix+"retries"], 10, 64); err == nil && n >= 0 {
					retries = strconv.FormatInt(n, 10)
				}
				switch fields[prefix+"source"] {
				case "none", "ANTHROPIC_API_KEY":
					if init == "true" {
						source = fields[prefix+"source"]
					}
				}
				if event != "none" {
					if n, err := strconv.ParseInt(fields[prefix+"age_ms"], 10, 64); err == nil && n >= 0 {
						age = strconv.FormatInt(n, 10)
					}
				}
			}
		}
		// Contradictions invalidate the participating observations and event age;
		// independent fields remain known and missing values are never inferred.
		conflictingInit := event == "none" && init == "true" || event == "init" && init == "false"
		conflictingRetries := event == "none" && retries != "unknown" && retries != "0" || event == "api_retry" && retries == "0"
		if conflictingInit {
			init, source = "unknown", "unknown"
		}
		if conflictingRetries {
			retries = "unknown"
		}
		if conflictingInit || conflictingRetries {
			event, age = "unknown", "unknown"
		}
		for _, entry := range []struct{ key, value string }{{"event", event}, {"age_ms", age}, {"init", init}, {"source", source}, {"retries", retries}} {
			safe = append(safe, prefix+entry.key+"="+entry.value)
		}
	}
	return safe
}
