package claudetape

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// PoolProcessConfig is a test-only, single fresh-child pipe boundary. The owner
// seals it while the original runner is still alive; EOF/exit/lifecycle recording
// is intentionally outside this profile.
type PoolProcessConfig struct {
	Mode, Scenario, Path, Home, WorkDir, Claude, Claim, Finish string
	Producer                                                   Producer
}

// RunPoolProcess forwards capture stdout continuously and replays only prefixes
// released by exact daemon/runner stdin. It never executes a provider in replay.
func RunPoolProcess(c PoolProcessConfig, args []string, input *os.File, output, diagnostic io.Writer) (int, error) {
	fail := errors.New("pool recording process failed")
	switch c.Scenario {
	case "pool-model-change", "pool-bypass-revoke", "pool-bypass-escalate", "pool-posture-gate":
	default:
		return 0, fail
	}
	if c.Mode != "capture" && c.Mode != "replay" {
		return 0, fail
	}
	actualCWD, err := os.Getwd()
	if err != nil || actualCWD != c.WorkDir || os.Getenv("HOME") != c.Home {
		return 0, errors.New("pool runner environment changed")
	}
	binding, err := NewPoolBinding(args, c.Home, c.WorkDir)
	if err != nil {
		return 0, err
	}
	files, err := binding.Files()
	if err != nil {
		return 0, err
	}
	if len(files["--settings"]) == 0 {
		return 0, errors.New("pool profile lacks complete settings file")
	}
	return runBoundProcess(c, args, binding, files, input, output, diagnostic)
}

type childBinding interface {
	Args() []string
	Frame([]byte, bool) ([]byte, error)
	Files() (map[string]json.RawMessage, error)
}

func runBoundProcess(c PoolProcessConfig, args []string, binding childBinding, files map[string]json.RawMessage, input *os.File, output, diagnostic io.Writer) (int, error) {
	fail := errors.New("child recording process failed")
	claim, err := os.OpenFile(c.Claim, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		_ = os.WriteFile(c.Claim+".duplicate", nil, 0600)
		return 0, errors.New("pool profile refuses additional child")
	}
	if err := claim.Close(); err != nil {
		return 0, err
	}
	fd := int(input.Fd())
	if syscall.SetNonblock(fd, true) != nil {
		return 0, fail
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	var tape *Tape
	var player *Player
	var nativeInput io.WriteCloser
	type line struct {
		raw []byte
		err error
	}
	var nativeLines <-chan line
	if c.Mode == "capture" {
		if !filepath.IsAbs(c.Claude) || (os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == "") || os.Getenv("CLAUDE_CONFIG_DIR") != "" {
			return 0, errors.New("pool capture lacks explicit provider")
		}
		cmd := exec.CommandContext(ctx, c.Claude, args...)
		cmd.Stderr = diagnostic
		cmd.WaitDelay = 2 * time.Second
		nativeInput, err = cmd.StdinPipe()
		if err != nil {
			return 0, fail
		}
		nativeOutput, err := cmd.StdoutPipe()
		if err != nil {
			return 0, fail
		}
		if cmd.Start() != nil {
			return 0, fail
		}
		defer func() { cancel(); _ = nativeInput.Close(); _ = cmd.Wait() }()
		ch := make(chan line)
		nativeLines = ch
		go func() {
			defer close(ch)
			scan := bufio.NewScanner(nativeOutput)
			scan.Buffer(make([]byte, 4096), MaxBytes)
			send := func(v line) bool {
				select {
				case ch <- v:
					return true
				case <-ctx.Done():
					return false
				}
			}
			for scan.Scan() {
				if !send(line{raw: append([]byte(nil), scan.Bytes()...)}) {
					return
				}
			}
			err := scan.Err()
			if err == nil {
				err = io.EOF
			}
			send(line{err: err})
		}()
		tape = &Tape{Version: 1, Scenario: c.Scenario, Producer: c.Producer, Args: binding.Args(), Files: files}
	} else {
		f, err := os.Open(c.Path)
		if err != nil {
			return 0, errors.New("missing reviewed pool tape")
		}
		tape, err = Decode(f)
		closeErr := f.Close()
		if err != nil {
			return 0, err
		}
		if closeErr != nil {
			return 0, closeErr
		}
		if tape.Scenario != c.Scenario {
			return 0, errors.New("pool tape role changed")
		}
		if err := tape.CheckFiles(files); err != nil {
			return 0, err
		}
		player, err = New(tape, binding.Args())
		if err != nil {
			return 0, err
		}
	}
	frames, total := 0, 0
	// Use one serializer for append-before-forward and byte/frame accounting.
	accept := func(direction string, raw []byte) error {
		normalized, err := binding.Frame(raw, false)
		if err != nil {
			return err
		}
		if player != nil {
			if direction != "stdin" {
				return fail
			}
			released, err := player.Input(normalized)
			if err != nil {
				return err
			}
			frames++
			for _, saved := range released {
				restored, err := binding.Frame(saved, true)
				if err != nil {
					return err
				}
				if _, err := output.Write(append(restored, '\n')); err != nil {
					return fail
				}
				frames++
			}
			return nil
		}
		total += len(normalized)
		if total > MaxBytes || len(tape.Frames) >= MaxFrames {
			return errors.New("pool recording exceeded bound")
		}
		if err := Safe(normalized); err != nil {
			return err
		}
		tape.Frames = append(tape.Frames, Frame{Direction: direction, Data: normalized})
		frames++
		target := output
		if direction == "stdin" {
			target = nativeInput
		}
		if _, err := target.Write(append(raw, '\n')); err != nil {
			return fail
		}
		return nil
	}
	if player != nil {
		for _, raw := range player.Prefix() {
			restored, err := binding.Frame(raw, true)
			if err != nil {
				return frames, err
			}
			if _, err := output.Write(append(restored, '\n')); err != nil {
				return frames, fail
			}
			frames++
		}
	}
	var pending []byte
	drain := func() error {
		var buf [4096]byte
		for {
			n, err := syscall.Read(fd, buf[:])
			if n > 0 {
				pending = append(pending, buf[:n]...)
				for {
					end := bytes.IndexByte(pending, '\n')
					if end < 0 {
						break
					}
					if end > MaxBytes {
						return fail
					}
					if err := accept("stdin", pending[:end]); err != nil {
						return err
					}
					pending = pending[end+1:]
				}
				if len(pending) > MaxBytes {
					return fail
				}
			}
			if err == syscall.EINTR {
				continue
			}
			if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
				return nil
			}
			if err != nil || n == 0 {
				return errors.New("pool runner stdin ended before seal")
			}
		}
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := drain(); err != nil {
			return frames, err
		}
		select {
		case <-ctx.Done():
			return frames, errors.New("pool recorder deadline exceeded")
		case <-tick.C:
			if _, err := os.Stat(c.Finish); err == nil {
				if err := drain(); err != nil {
					return frames, err
				}
				if len(pending) != 0 {
					return frames, errors.New("partial pool input at seal")
				}
				if player != nil {
					return frames, player.Complete()
				}
				return frames, tape.Save(c.Path)
			}
		case v := <-nativeLines:
			if v.err != nil || v.raw == nil {
				return frames, errors.New("pool native stdout ended before seal")
			}
			if err := accept("stdout", v.raw); err != nil {
				return frames, err
			}
		}
	}
}
