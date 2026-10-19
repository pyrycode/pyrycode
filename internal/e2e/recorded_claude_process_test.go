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
)

type tapeProcessConfig struct {
	Mode      string
	Path      string
	Home      string
	Finish    string
	Receipt   string
	Claim     string
	Claude    string
	Scenario  string
	Daemon    string
	Socket    string
	MCPConfig string
	Producer  claudetape.Producer
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
	if errors.Is(err, errTapeDuplicate) {
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
	if c.Scenario != "permission-deny" && c.Scenario != "permission-allow" {
		return nil
	}
	wantPath := "${PILOT_HOME}/tape-denied.txt"
	if c.Scenario == "permission-allow" {
		wantPath = "${PILOT_HOME}/tape-allowed.txt"
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
	if len(files) != 3 {
		return nil, errors.New("incomplete pilot Claude input files")
	}
	return files, nil
}

var errTapeDuplicate = errors.New("pilot allows exactly one Claude process")

func runClaudeTapeProcess(c tapeProcessConfig, args []string) (int, error) {
	claim, err := os.OpenFile(c.Claim, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.WriteFile(c.Claim+".duplicate", nil, 0o600)
		return 0, errTapeDuplicate
	}
	_ = claim.Close()
	files, err := tapeFiles(args, &c)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
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
	frames := 0
	bytes := 0
	emit := func(lines []json.RawMessage) error {
		for _, line := range lines {
			if err := checkTapePermission(line, c); err != nil {
				return err
			}
			data, e := tapeNormalize(line, c, true)
			if e != nil {
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
	}
	handleInput := func(raw []byte) error {
		normalized, e := tapeNormalize(raw, c, false)
		if e != nil {
			return e
		}
		if player != nil {
			out, e := player.Input(normalized)
			if e != nil {
				return e
			}
			frames++
			if e = emit(out); e != nil {
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
	for {
		if err := inputs.drain(handleInput); err != nil {
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
				if player != nil {
					return frames, player.Complete()
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
			if _, e = os.Stdout.Write(append(line.data, '\n')); e != nil {
				return frames, errors.New("claude stdout delivery failed")
			}
		}
	}
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
