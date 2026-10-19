//go:build e2e

package e2e

import (
	"bufio"
	"context"
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

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

type tapeProcessConfig struct {
	Mode     string
	Path     string
	Home     string
	Finish   string
	Receipt  string
	Claim    string
	Claude   string
	Scenario string
	Producer claudetape.Producer
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

func tapeNormalize(raw []byte, home string, restore bool) ([]byte, error) {
	var value any
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	if d.Decode(&value) != nil {
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
			if restore {
				return strings.ReplaceAll(x, "${PILOT_HOME}", home)
			}
			return strings.ReplaceAll(x, home, "${PILOT_HOME}")
		}
		return v
	}
	return json.Marshal(walk(value))
}

func tapeArgs(args []string, home string) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = strings.ReplaceAll(arg, home, "${PILOT_HOME}")
	}
	return out
}

func runClaudeTapeProcess(c tapeProcessConfig, args []string) (int, error) {
	claim, err := os.OpenFile(c.Claim, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, errors.New("pilot allows exactly one Claude process")
	}
	_ = claim.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	inputs := tapeLines(ctx, os.Stdin)
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
			return 0, errors.New("Claude stdin setup failed")
		}
		stdout, e := child.StdoutPipe()
		if e != nil {
			return 0, errors.New("Claude stdout setup failed")
		}
		if child.Start() != nil {
			return 0, errors.New("Claude launch failed")
		}
		defer func() { cancel(); _ = stdin.Close(); _ = child.Wait() }()
		outputs = tapeLines(ctx, stdout)
		tape = &claudetape.Tape{Version: 1, Scenario: c.Scenario, Producer: c.Producer, Args: tapeArgs(args, c.Home)}
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
			return 0, errors.New("Claude tape scenario mismatch")
		}
		player, err = claudetape.New(tape, tapeArgs(args, c.Home))
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
			data, e := tapeNormalize(line, c.Home, true)
			if e != nil {
				return e
			}
			if _, e = os.Stdout.Write(append(data, '\n')); e != nil {
				return errors.New("Claude replay delivery failed")
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
	for {
		select {
		case <-ctx.Done():
			return frames, errors.New("Claude tape deadline exceeded")
		case <-tick.C:
			if _, err := os.Stat(c.Finish); err == nil {
				if player != nil {
					return frames, player.Complete()
				}
				if err := tape.Save(c.Path); err != nil {
					return frames, err
				}
				return frames, nil
			}
		case line := <-inputs:
			if line.err != nil || line.data == nil {
				return frames, errors.New("daemon stdin ended before scenario seal")
			}
			normalized, e := tapeNormalize(line.data, c.Home, false)
			if e != nil {
				return frames, e
			}
			if player != nil {
				out, e := player.Input(normalized)
				if e != nil {
					return frames, e
				}
				frames++
				if e = emit(out); e != nil {
					return frames, e
				}
			} else {
				bytes += len(normalized)
				if bytes > claudetape.MaxBytes || len(tape.Frames) >= claudetape.MaxFrames {
					return frames, errors.New("capture exceeded bound")
				}
				if e = claudetape.Safe(normalized); e != nil {
					return frames, fmt.Errorf("stdin frame %d: %w", frames, e)
				}
				tape.Frames = append(tape.Frames, claudetape.Frame{Direction: "stdin", Data: normalized})
				frames++
				if _, e = stdin.Write(append(line.data, '\n')); e != nil {
					return frames, errors.New("Claude stdin delivery failed")
				}
			}
		case line := <-outputs:
			if line.err != nil || line.data == nil {
				return frames, errors.New("Claude stdout ended before scenario seal")
			}
			normalized, e := tapeNormalize(line.data, c.Home, false)
			if e != nil {
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
				return frames, errors.New("Claude stdout delivery failed")
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
