//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
	scenariohelpers "github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
)

type tapeProcessConfig struct {
	CheckpointDir   string
	ObserverAddress string
	Mode            string
	Path            string
	Home            string
	Finish          string
	Receipt         string
	Claim           string
	Claude          string
	Scenario        string
	Daemon          string
	Socket          string
	MCPConfig       string
	Producer        claudetape.Producer
	// Ready is created once a replay has delivered every frame before the
	// daemon's first user turn. The scenario driver waits for it in replay.
	Ready string
	// HoldInitialize delays the replayed reply to the initialize request,
	// modelling a Claude that answers its posture before its initialize. It
	// exists for the startup-order regression test only.
	HoldInitialize time.Duration
}

type tapeReceipt struct {
	Success bool   `json:"success"`
	Frames  int    `json:"frames"`
	Error   string `json:"error,omitempty"`
}

// The helper process replaces only the supervised Claude executable. os.Exit
// prevents Go's test runner from writing its own output into the JSONL stream.
func TestClaudeTapeProcess(t *testing.T) {
	path := os.Getenv("PYRY_CLAUDE_TAPE_PROCESS")
	if path == "" {
		return
	}
	var c tapeProcessConfig
	raw, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(raw, &c)
	}
	if err != nil {
		os.Exit(2)
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	frames, err := runClaudeTapeProcess(c, args)
	if errors.Is(err, errTapeDuplicate) || errors.Is(err, errTapeNativePrintRefused) {
		os.Exit(1)
	}
	receipt := tapeReceipt{Success: err == nil, Frames: frames}
	if err != nil {
		receipt.Error = err.Error()
	}
	data, _ := json.Marshal(receipt)
	if os.WriteFile(c.Receipt+".tmp", data, 0o600) != nil {
		os.Exit(2)
	}
	if os.Rename(c.Receipt+".tmp", c.Receipt) != nil {
		os.Exit(2)
	}
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

type tapeLine struct {
	data []byte
	err  error
}

func tapeLines(ctx context.Context, r io.Reader) <-chan tapeLine {
	ch := make(chan tapeLine)
	go func() {
		defer close(ch)
		scan := bufio.NewScanner(r)
		scan.Buffer(make([]byte, 4096), claudetape.MaxBytes)
		for scan.Scan() {
			line := append([]byte(nil), scan.Bytes()...)
			select {
			case ch <- tapeLine{data: line}:
			case <-ctx.Done():
				return
			}
		}
		err := scan.Err()
		if err == nil {
			err = io.EOF
		}
		select {
		case ch <- tapeLine{err: err}:
		case <-ctx.Done():
		}
	}()
	return ch
}

// tapeInput owns stdin reads so sealing cannot race a separate reader goroutine.
// A drain's EAGAIN marks the captured prefix; later input is outside that prefix.
type tapeInput struct {
	fd      int
	pending []byte
}

func newTapeInput(f *os.File) (*tapeInput, error) {
	fd := int(f.Fd())
	if err := syscall.SetNonblock(fd, true); err != nil {
		return nil, errors.New("cannot poll daemon stdin")
	}
	return &tapeInput{fd: fd}, nil
}

func (input *tapeInput) drain(accept func([]byte) error) error {
	var buffer [4096]byte
	for {
		n, err := syscall.Read(input.fd, buffer[:])
		if n > 0 {
			input.pending = append(input.pending, buffer[:n]...)
			for {
				end := bytes.IndexByte(input.pending, '\n')
				if end < 0 {
					break
				}
				if end > claudetape.MaxBytes {
					return errors.New("daemon stdin frame exceeded bound")
				}
				if err := accept(input.pending[:end]); err != nil {
					return err
				}
				input.pending = input.pending[end+1:]
			}
			if len(input.pending) > claudetape.MaxBytes {
				return errors.New("daemon stdin frame exceeded bound")
			}
		}
		if err == syscall.EINTR {
			continue
		}
		if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
			return nil
		}
		if err != nil || n == 0 {
			return errors.New("daemon stdin ended before scenario seal")
		}
	}
}

func checkTapePermission(raw []byte, c tapeProcessConfig) error {
	var wantPath string
	switch c.Scenario {
	case "permission-deny":
		wantPath = "${PILOT_HOME}/tape-denied.txt"
	case "permission-allow":
		wantPath = "${PILOT_HOME}/tape-allowed.txt"
	case "permission-cancel":
		wantPath = "${PILOT_HOME}/pyrycode-cancel-denied-witness.txt"
	default:
		return nil
	}
	var frame struct {
		Type    string `json:"type"`
		Request struct {
			Subtype  string `json:"subtype"`
			ToolName string `json:"tool_name"`
			Input    struct {
				FilePath string `json:"file_path"`
			} `json:"input"`
		} `json:"request"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		return errors.New("invalid permission frame")
	}
	if frame.Type == "control_request" && frame.Request.Subtype == "can_use_tool" &&
		(frame.Request.ToolName != "Write" || frame.Request.Input.FilePath != wantPath) {
		return errors.New("permission scenario requested a different tool or target")
	}
	return nil
}

func tapeNormalize(raw []byte, c tapeProcessConfig, restore bool) ([]byte, error) {
	var value any
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	if d.Decode(&value) != nil || d.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid Claude JSONL")
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, value := range x {
				x[k] = walk(value)
			}
		case []any:
			for i, value := range x {
				x[i] = walk(value)
			}
		case string:
			return tapeString(x, c, restore)
		}
		return v
	}
	return json.Marshal(walk(value))
}

func tapeString(s string, c tapeProcessConfig, restore bool) string {
	for _, binding := range []struct{ value, label string }{{c.MCPConfig, "${PILOT_MCP_CONFIG}"}, {c.Daemon, "${PILOT_DAEMON}"}, {c.Socket, "${PILOT_SOCKET}"}, {c.Home, "${PILOT_HOME}"}} {
		if binding.value == "" {
			continue
		}
		if restore {
			s = strings.ReplaceAll(s, binding.label, binding.value)
		} else {
			s = strings.ReplaceAll(s, binding.value, binding.label)
		}
	}
	return s
}

func tapeArgs(args []string, c tapeProcessConfig) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = tapeString(arg, c, false)
	}
	return out
}

func tapeFiles(args []string, c *tapeProcessConfig) (map[string]json.RawMessage, error) {
	for i, arg := range args {
		if arg == "--mcp-config" && i+1 < len(args) {
			c.MCPConfig = args[i+1]
		}
	}
	if c.Scenario == "mcp-reconnect" {
		if c.MCPConfig == "" {
			return nil, errors.New("MCP reconnect profile requires the production config path")
		}
		if err := scenariohelpers.InjectMCPReconnectServer(c.MCPConfig, filepath.Join(c.Home, "reconnect-command")); err != nil {
			return nil, errors.New("MCP reconnect config preparation failed")
		}
	}
	files := map[string]json.RawMessage{}
	for i, arg := range args {
		if arg != "--settings" && arg != "--mcp-config" && arg != "--append-system-prompt-file" {
			continue
		}
		if i+1 >= len(args) || !filepath.IsAbs(args[i+1]) {
			return nil, errors.New("pilot requires file-based Claude inputs")
		}
		f, err := os.Open(args[i+1])
		if err != nil {
			return nil, errors.New("missing Claude input file")
		}
		raw, err := io.ReadAll(io.LimitReader(f, claudetape.MaxBytes+1))
		_ = f.Close()
		if err != nil || len(raw) > claudetape.MaxBytes {
			return nil, errors.New("claude input file exceeded bound")
		}
		if arg == "--append-system-prompt-file" {
			raw, err = json.Marshal(string(raw))
			if err != nil {
				return nil, err
			}
		}
		normalized, err := tapeNormalize(raw, *c, false)
		if err != nil {
			return nil, err
		}
		if err = claudetape.Safe(normalized); err != nil {
			return nil, err
		}
		if _, exists := files[arg]; exists {
			return nil, errors.New("duplicate Claude input file flag")
		}
		files[arg] = normalized
	}
	// Operator-bypass spawns omit approval MCP configuration. Capture every
	// supplied known file, and always require settings plus the system prompt;
	// replay/fresh comparison still rejects any changed file or argv inventory.
	if len(files["--settings"]) == 0 || len(files["--append-system-prompt-file"]) == 0 {
		return nil, errors.New("incomplete pilot Claude input files")
	}
	switch c.Scenario {
	case "question-answer-witness", "bypass-witness-mcp-stored", "bypass-witness-mcp-operator", "bypass-witness-stdio-stored", "bypass-witness-stdio-operator", "ordinary-queue-placement", "interrupt-running", "interrupt-thinking", "subagent-text", "hook-banner", "workspace-change", "skill-silence", "confirmed-permission-settings", "default-outside-read", "mcp-reconnect", "permission-context-absent", "permission-context-outside", "native-suggestions":
		cwd, err := os.Getwd()
		if err != nil {
			return nil, errors.New("scenario cwd unavailable")
		}
		raw, _ := json.Marshal(cwd)
		normalized, err := tapeNormalize(raw, *c, false)
		if err != nil {
			return nil, err
		}
		if err = claudetape.Safe(normalized); err != nil {
			return nil, err
		}
		files["$cwd"] = normalized
	}
	if c.Scenario == "hook-banner" {
		for key, path := range map[string]string{"$hook/user-settings": filepath.Join(c.Home, ".claude", "settings.json"), "$hook/script": filepath.Join(c.Home, scenariohelpers.HookRigDir, "hook.sh")} {
			raw, err := os.ReadFile(path)
			if err != nil || len(raw) > claudetape.MaxBytes {
				return nil, errors.New("hook input unavailable or oversized")
			}
			info, err := os.Stat(path)
			if err != nil {
				return nil, errors.New("hook input mode unavailable")
			}
			encoded, _ := json.Marshal(struct {
				Text string
				Mode uint32
			}{string(raw), uint32(info.Mode().Perm())})
			normalized, err := tapeNormalize(encoded, *c, false)
			if err != nil {
				return nil, err
			}
			if err = claudetape.Safe(normalized); err != nil {
				return nil, err
			}
			files[key] = normalized
		}
	}
	if c.Scenario == "skill-silence" {
		raw, err := os.ReadFile(filepath.Join(c.Home, ".claude", "skills", scenariohelpers.SkillName, "SKILL.md"))
		if err != nil || len(raw) > claudetape.MaxBytes {
			return nil, errors.New("scenario skill input unavailable or oversized")
		}
		encoded, _ := json.Marshal(string(raw))
		normalized, err := tapeNormalize(encoded, *c, false)
		if err != nil {
			return nil, err
		}
		if err = claudetape.Safe(normalized); err != nil {
			return nil, err
		}
		files["$skill/pyry-probe"] = normalized
	}
	if c.Scenario == "native-suggestions" {
		policy, _ := json.Marshal("reject-non-stream-before-producer-claim")
		files["$native/non-stream-policy"] = policy
		enabled, _ := json.Marshal(os.Getenv("CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION"))
		files["$native/enable"] = enabled
	}
	return files, nil
}

var errTapeDuplicate = errors.New("pilot allows exactly one Claude process")
var errTapeNativePrintRefused = errors.New("native profile refuses non-stream invocation")

func nativeStreamArgs(args []string) bool {
	for i, arg := range args {
		if arg == "--input-format" && i+1 < len(args) && args[i+1] == "stream-json" {
			return true
		}
	}
	return false
}

func runClaudeTapeProcess(c tapeProcessConfig, args []string) (int, error) {
	if c.Scenario == "native-suggestions" && !nativeStreamArgs(args) {
		return 0, errTapeNativePrintRefused
	}
	if c.Scenario == "native-suggestions" {
		if err := os.Setenv("CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION", "1"); err != nil {
			return 0, errors.New("native suggestion enable unavailable")
		}
	}
	claim, err := os.OpenFile(c.Claim, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.WriteFile(c.Claim+".duplicate", nil, 0o600)
		return 0, errTapeDuplicate
	}
	_ = claim.Close()
	if (c.CheckpointDir != "") != claudetape.RequiresClientCheckpoint(c.Scenario) {
		return 0, errors.New("client checkpoint configuration changed")
	}
	files, err := tapeFiles(args, &c)
	if err != nil {
		return 0, err
	}
	budget := 30 * time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	inputs, err := newTapeInput(os.Stdin)
	if err != nil {
		return 0, err
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	var player *claudetape.Player
	var tape *claudetape.Tape
	var child *exec.Cmd
	var stdin io.WriteCloser
	var outputs <-chan tapeLine
	if c.Mode == "capture" {
		if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
			return 0, errors.New("capture requires explicit Claude credentials")
		}
		if !filepath.IsAbs(c.Claude) {
			return 0, errors.New("capture requires an absolute Claude executable")
		}
		child = exec.CommandContext(ctx, c.Claude, args...)
		child.Stderr = os.Stderr
		child.WaitDelay = 2 * time.Second
		stdin, err = child.StdinPipe()
		if err != nil {
			return 0, errors.New("claude stdin setup failed")
		}
		stdout, e := child.StdoutPipe()
		if e != nil {
			return 0, errors.New("claude stdout setup failed")
		}
		if child.Start() != nil {
			return 0, errors.New("claude launch failed")
		}
		defer func() { cancel(); _ = stdin.Close(); _ = child.Wait() }()
		outputs = tapeLines(ctx, stdout)
		tape = &claudetape.Tape{Version: 1, Scenario: c.Scenario, Producer: c.Producer, Args: tapeArgs(args, c), Files: files}
	} else if c.Mode == "replay" {
		f, e := os.Open(c.Path)
		if e != nil {
			return 0, errors.New("missing reviewed Claude tape")
		}
		tape, err = claudetape.Decode(f)
		_ = f.Close()
		if err != nil {
			return 0, err
		}
		if err = checkTapeCheckpoint(tape); err != nil {
			return 0, err
		}
		if tape.Scenario != c.Scenario {
			return 0, errors.New("claude tape scenario mismatch")
		}
		if err = tape.CheckFiles(files); err != nil {
			return 0, err
		}
		player, err = claudetape.New(tape, tapeArgs(args, c))
		if err != nil {
			return 0, err
		}
	} else {
		return 0, errors.New("unknown Claude tape mode")
	}
	if claudetape.RequiresClientCheckpoint(c.Scenario) && c.Mode == "capture" {
		tape.Version = 2
	}
	checkpoint := recordedCheckpointState{dir: c.CheckpointDir}
	frames := 0
	bytes := 0
	finalInputSeen := c.Scenario != "native-suggestions"
	initializeID := ""
	holdPending := c.HoldInitialize > 0
	ready := false
	signalReady := func() error {
		if ready || player == nil || c.Ready == "" || !player.AwaitsTurn() {
			return nil
		}
		ready = true
		if os.WriteFile(c.Ready, nil, 0o600) != nil {
			return errors.New("claude replay readiness signal failed")
		}
		return nil
	}

	emit := func(lines []json.RawMessage) error {
		for _, line := range lines {
			if err := checkTapePermission(line, c); err != nil {
				return err
			}
			if holdPending && initializeID != "" && answersRequest(line, initializeID) {
				holdPending = false
				time.Sleep(c.HoldInitialize)
			}
			data, e := tapeNormalize(line, c, true)
			if e != nil {
				return e
			}
			if e = publishRecordedSource(c, data); e != nil {
				return e
			}
			if _, e = os.Stdout.Write(append(data, '\n')); e != nil {
				return errors.New("claude replay delivery failed")
			}
			frames++
		}
		return nil
	}
	if player != nil {
		if err := emit(player.Prefix()); err != nil {
			return frames, err
		}
		if err := signalReady(); err != nil {
			return frames, err
		}
	}
	handleInput := func(raw []byte) error {
		if c.Scenario == "native-suggestions" && claudetape.IsUserText(raw, scenariohelpers.NativeSuggestionClearPrompt) {
			finalInputSeen = true
		}
		normalized, e := tapeNormalize(raw, c, false)
		if e != nil {
			return e
		}
		if player != nil {
			if holdPending && initializeID == "" {
				initializeID = initializeRequestID(raw)
			}
			out, e := player.Input(normalized)
			if e != nil {
				return e
			}
			frames++
			if e = emit(out); e != nil {
				return e
			}
			if e = signalReady(); e != nil {
				return e
			}
		} else {
			bytes += len(normalized)
			if bytes > claudetape.MaxBytes || len(tape.Frames) >= claudetape.MaxFrames {
				return errors.New("capture exceeded bound")
			}
			if e = claudetape.Safe(normalized); e != nil {
				return fmt.Errorf("stdin frame %d: %w", frames, e)
			}
			tape.Frames = append(tape.Frames, claudetape.Frame{Direction: "stdin", Data: normalized})
			frames++
			if _, e = stdin.Write(append(raw, '\n')); e != nil {
				return errors.New("claude stdin delivery failed")
			}
		}

		return nil
	}
	pollCheckpoint := func() error {
		return checkpoint.poll(tape, player, func(raw []byte) ([]byte, error) { return tapeNormalize(raw, c, false) }, emit, &bytes, &frames, func() error { return inputs.drain(handleInput) })
	}
	for {
		// Consume already-written native stdin before a newly published client
		// observation. Otherwise the file can overtake its preceding pipe write
		// merely because both become ready before this helper is scheduled.
		if err := inputs.drain(handleInput); err != nil {
			return frames, err
		}
		if err := pollCheckpoint(); err != nil {
			return frames, err
		}
		select {
		case <-ctx.Done():
			return frames, errors.New("claude tape deadline exceeded")
		case <-tick.C:
			if _, err := os.Stat(c.Finish); err == nil {
				if err := inputs.drain(handleInput); err != nil {
					return frames, err
				}
				if len(inputs.pending) != 0 {
					return frames, errors.New("unterminated daemon input at scenario seal")
				}
				if !finalInputSeen {
					continue
				}
				if err := pollCheckpoint(); err != nil {
					return frames, err
				}
				if player != nil {
					if err := player.Complete(); err != nil {
						return frames, err
					}
					return frames, checkpoint.seal()
				}
				if err := checkTapeCheckpoint(tape); err != nil {
					return frames, err
				}
				if err := checkpoint.seal(); err != nil {
					return frames, err
				}
				if err := tape.Save(c.Path); err != nil {
					return frames, err
				}
				return frames, nil
			}
		case line := <-outputs:
			if line.err != nil || line.data == nil {
				return frames, errors.New("claude stdout ended before scenario seal")
			}
			normalized, e := tapeNormalize(line.data, c, false)
			if e != nil {
				return frames, e
			}
			if e = checkTapePermission(normalized, c); e != nil {
				return frames, e
			}
			bytes += len(normalized)
			if bytes > claudetape.MaxBytes || len(tape.Frames) >= claudetape.MaxFrames {
				return frames, errors.New("capture exceeded bound")
			}
			if e = claudetape.Safe(normalized); e != nil {
				return frames, fmt.Errorf("stdout frame %d: %w", frames, e)
			}
			tape.Frames = append(tape.Frames, claudetape.Frame{Direction: "stdout", Data: normalized})
			frames++
			if e = publishRecordedSource(c, line.data); e != nil {
				return frames, e
			}
			if _, e = os.Stdout.Write(append(line.data, '\n')); e != nil {
				return frames, errors.New("claude stdout delivery failed")
			}
		}
	}
}

// initializeRequestID returns the request id of an initialize control request.
func initializeRequestID(raw []byte) string {
	var frame struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	if json.Unmarshal(raw, &frame) != nil || frame.Type != "control_request" || frame.Request.Subtype != "initialize" {
		return ""
	}
	return frame.RequestID
}

// answersRequest reports whether raw is the control response to requestID.
func answersRequest(raw []byte, requestID string) bool {
	var frame struct {
		Type     string `json:"type"`
		Response struct {
			RequestID string `json:"request_id"`
		} `json:"response"`
	}
	return json.Unmarshal(raw, &frame) == nil && frame.Type == "control_response" && frame.Response.RequestID == requestID
}

func tapeWrapper(t *testing.T, c tapeProcessConfig) string {
	t.Helper()
	config := filepath.Join(c.Home, "tape-config.json")
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(config, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	path := filepath.Join(c.Home, "claude-tape")
	script := fmt.Sprintf("#!/bin/sh\nexport PYRY_CLAUDE_TAPE_PROCESS=%s\nexec %s -test.run=^TestClaudeTapeProcess$ -- \"$@\"\n", quote(config), quote(executable))
	if err = os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// The busy-turn scenario must not be promoted unless a real daemon stdin write
// gates the recorded tool result. Apply the same audit on every replay.
func checkTapeCheckpoint(tape *claudetape.Tape) error {
	switch tape.Scenario {
	case "ordinary-queue-placement":
		return tape.RequireQueueAdmission(tapeConversation, recordedOrdinaryQueuePrompt, scenariohelpers.OrdinaryQueuedText)
	case "interrupt-running":
		return tape.RequireInterruptBoundary(tapeConversation, recordedRunningInterruptPrompt, recordedInterruptHealthPrompt, false)
	case "interrupt-thinking":
		return tape.RequireInterruptBoundary(tapeConversation, scenariohelpers.InitialThinkingPrompt("tape-interrupt-thinking"), "", true)
	case "subagent-text":
		return tape.RequireSubagentMainObservation(tapeConversation, recordedSubagentPrompt)
	}

	if tape.Scenario == "hook-banner" {
		return tape.RequireHookOrder(scenariohelpers.HookBlockPrompt, scenariohelpers.HookLivePrompt)
	}
	if tape.Scenario == "native-suggestions" {
		if err := tape.RequireNativeSuggestion(); err != nil {
			return err
		}
		return tape.RequireUserText(scenariohelpers.NativeSuggestionClearPrompt)
	}
	if tape.Scenario == "default-outside-read" {
		return tape.RequireModeBeforeUser("default", 1)
	}
	if tape.Scenario == "confirmed-permission-settings" {
		return tape.RequireModeBeforeUser("default", 2)
	}
	if strings.HasPrefix(tape.Scenario, "bypass-questions-") || strings.HasPrefix(tape.Scenario, "bypass-witness-") {
		return tape.RequireBypassQuestions()
	}
	if tape.Scenario == "send-now-placement" {
		return tape.RequireMidTurnInput(scenariohelpers.SendNowQueuedText, "Bash")
	}
	return nil
}
