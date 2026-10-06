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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
record({"calls": 1, "start_ms": time.time_ns() // 1000000})
try:
    child = subprocess.Popen([real_cli] + args, stdout=subprocess.PIPE)
except OSError:
    record({"completed": 1, "elapsed_ms": int((time.monotonic() - started) * 1000)})
    sys.exit(1)
retained = bytearray()
size = 0
while True:
    chunk = os.read(child.stdout.fileno(), 8192)
    if not chunk:
        break
    size += len(chunk)
    retained.extend(chunk[:max(0, 4097 - len(retained))])
    sys.stdout.buffer.write(chunk)
    sys.stdout.buffer.flush()
code = child.wait()
fields = {"completed": 1, "elapsed_ms": int((time.monotonic() - started) * 1000),
          "exit_code": code, "stdout_bytes": size, "output_observed": True,
          "utf8_ok": False, "json_ok": False, "result_ok": False, "text_ok": False}
try:
    raw = retained.decode("utf-8")
    fields["utf8_ok"] = True
    if size <= 4096:
        def reject_constant(value):
            raise ValueError()
        result = json.loads(raw, parse_constant=reject_constant,
                            object_pairs_hook=lambda pairs: ("object", pairs))
        if isinstance(result, tuple):
            pairs = [(k.casefold(), v) for k, v in result[1]]
            # Go reports a type error even if a later duplicate is well typed.
            for k, v in pairs:
                if ((k in ("result", "subtype") and v is not None and not isinstance(v, str)) or
                        (k == "is_error" and v is not None and not isinstance(v, bool))):
                    raise ValueError()
            # encoding/json matches these struct fields case-insensitively.
            # JSON null leaves Go's existing string/bool struct field unchanged.
            result = {}
            for k, v in pairs:
                if v is not None:
                    result[k] = v
            text = result.get("result")
            subtype = result.get("subtype")
            text = "" if text is None else text
            subtype = "" if subtype is None else subtype
            is_error = result.get("is_error")
            if (isinstance(text, str) and isinstance(subtype, str) and
                    (is_error is None or isinstance(is_error, bool))):
                fields["json_ok"] = True
                fields["result_ok"] = not is_error and subtype in ("", "success")
                # encoding/json replaces unpaired escaped UTF-16 surrogates.
                text = text.encode("utf-16", "surrogatepass").decode("utf-16", "replace")
                # Match strings.TrimSpace, not Python's broader C0 whitespace.
                text = text.strip("\t\n\v\f\r \u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000")
                fields["text_ok"] = (0 < len(text) <= 240 and len(text.encode("utf-8")) <= 1024 and
                    not any(unicodedata.category(c) == "Cc" or c in "\u2028\u2029" for c in text))
except (ValueError, TypeError, AttributeError, UnicodeError):
    pass # Only fixed predicates survive; never report exception/output text.
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
	case s.Calls != 1 || s.Completed > s.Calls:
		return "unknown (ambiguous invocation evidence)"
	case s.Completed == 0:
		return "incomplete invocation (exit/output unknown)"
	case s.ExitCode == nil:
		return "unknown (no observed child exit)"
	case *s.ExitCode != 0:
		return "unsuccessful invocation"
	case !s.OutputObserved:
		return "unknown (no output validation evidence)"
	case s.StdoutBytes > 4096 || !s.UTF8OK || !s.JSONOK || !s.ResultOK || !s.TextOK:
		return "unusable output"
	default:
		return "usable output without wire set"
	}
}

// diagnostic uses only fixed labels and scalar metadata, never source values.
func (s suggestSource) diagnostic(idle bool, setRev, clearRev uint64) string {
	exit, output := "unknown", "unknown"
	if s.ExitCode != nil {
		exit = fmt.Sprint(*s.ExitCode)
	}
	if s.OutputObserved {
		output = fmt.Sprintf("bytes=%d utf8=%v json=%v result_success=%v text_valid=%v",
			s.StdoutBytes, s.UTF8OK, s.JSONOK, s.ResultOK, s.TextOK)
	}
	elapsed := s.ElapsedMS
	if s.StartMS != 0 && s.Completed == 0 {
		elapsed = max(0, time.Now().UnixMilli()-s.StartMS)
	}
	return fmt.Sprintf("fallback source: streams=%d results=%d idle=%v calls=%d completed=%d elapsed_ms=%d exit=%s output={%s} set_revision=%d clear_revision=%d stage=%s",
		s.Streams, s.Results, idle, s.Calls, s.Completed, elapsed, exit, output, setRev, clearRev, s.stage(setRev != 0))
}

func readSuggestSource(t *testing.T, path string) suggestSource {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal("open native source evidence failed")
	}
	defer f.Close()
	var total suggestSource
	dec := json.NewDecoder(io.LimitReader(f, 64*1024))
	for {
		var entry suggestSource
		if err := dec.Decode(&entry); errors.Is(err, io.EOF) {
			return total
		} else if errors.Is(err, io.ErrUnexpectedEOF) {
			return total // The observer may be midway through its final append.
		} else if err != nil {
			t.Fatal("decode source metadata failed")
		}
		total.Streams += entry.Streams
		total.Results += entry.Results
		total.Events += entry.Events
		total.Suggestions += entry.Suggestions
		total.Bytes += entry.Bytes
		total.Warnings += entry.Warnings
		if entry.Calls != 0 {
			// Start a fresh observation without carrying the previous output flags.
			streams, results, events, suggestions, bytes, warnings := total.Streams, total.Results, total.Events, total.Suggestions, total.Bytes, total.Warnings
			calls, completed := total.Calls+entry.Calls, total.Completed
			total = entry
			total.Streams, total.Results, total.Events, total.Suggestions, total.Bytes, total.Warnings = streams, results, events, suggestions, bytes, warnings
			total.Calls, total.Completed = calls, completed
		}
		if entry.Completed != 0 {
			total.Completed += entry.Completed
			total.ElapsedMS, total.ExitCode, total.StdoutBytes = entry.ElapsedMS, entry.ExitCode, entry.StdoutBytes
			total.OutputObserved, total.UTF8OK, total.JSONOK = entry.OutputObserved, entry.UTF8OK, entry.JSONOK
			total.ResultOK, total.TextOK = entry.ResultOK, entry.TextOK
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
	logSource := func() { t.Log(readSuggestSource(t, evidence).diagnostic(w.idle, w.setRev, w.clearRev)) }
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
