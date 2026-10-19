package claudetape

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CLIProcessConfig owns exactly one native invocation. SnapshotPath derives the
// real session path through the original test's production path helper.
type CLIProcessConfig struct {
	Mode, Scenario, Path, Home, Claude, Claim string
	Producer                                  Producer
	DaemonSHA256                              string
	SnapshotPath                              func(string) string `json:"-"`
}

func RunCLIProcess(c CLIProcessConfig, args []string, input io.Reader, output, diagnostic io.Writer) (int, error) {
	fail := errors.New("CLI process recording failed")
	cwd, err := os.Getwd()
	if err != nil || cwd != c.Home || os.Getenv("HOME") != c.Home || CLIProfiles()[c.Scenario] == "" || c.SnapshotPath == nil || (c.Mode != "capture" && c.Mode != "replay") {
		return 0, fail
	}
	b, err := NewCLIBinding(args, c.Home, cwd)
	if err != nil {
		return 0, err
	}
	files, err := b.Files()
	if err != nil {
		return 0, err
	}
	claim, err := os.OpenFile(c.Claim, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, fail
	}
	if claim.Close() != nil {
		return 0, fail
	}
	// streamrunner closes this one request before waiting for native output. EOF
	// is part of the contract, not inferred from a parseable JSON prefix.
	type readResult struct {
		raw []byte
		err error
	}
	read := make(chan readResult, 1)
	go func() { raw, err := io.ReadAll(io.LimitReader(input, MaxBytes+1)); read <- readResult{raw, err} }()
	var raw []byte
	select {
	case r := <-read:
		if r.err != nil || len(r.raw) > MaxBytes {
			return 0, fail
		}
		raw = r.raw
	case <-time.After(30 * time.Second):
		return 0, fail
	}
	if _, err := object(raw); err != nil {
		return 0, fail
	}
	if c.Mode == "replay" {
		f, err := os.Open(c.Path)
		if err != nil {
			return 0, fail
		}
		t, err := DecodeCLI(f)
		_ = f.Close()
		if err != nil || t.Scenario != c.Scenario || b.Check(t, string(raw), files) != nil {
			return 0, fail
		}
		for _, frame := range t.Frames {
			w := output
			if frame.Stream == "stderr" {
				w = diagnostic
			}
			if _, err := io.WriteString(w, b.Text(frame.Data, true)); err != nil {
				return 0, fail
			}
		}
		if t.Snapshot.Exists {
			path, err := cliSessionPath(c, t.SessionID)
			if err != nil {
				return 0, err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return 0, fail
			}
			f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(t.Snapshot.Mode))
			if err != nil {
				return 0, fail
			}
			_, writeErr := io.WriteString(f, b.Text(t.Snapshot.Data, true))
			closeErr := f.Close()
			if writeErr != nil || closeErr != nil {
				return 0, fail
			}
		}
		return t.ExitCode, nil
	}
	if os.Getenv("CLAUDE_CONFIG_DIR") != "" || (os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == "") || !filepath.IsAbs(c.Claude) {
		return 0, fail
	}
	t := &CLITape{Version: 1, Scenario: c.Scenario, Producer: c.Producer, DaemonSHA256: c.DaemonSHA256, Args: b.Args(), Files: files, Input: b.Text(string(raw), false), InputEOF: true}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Claude, args...)
	cmd.Dir = c.Home
	cmd.Stdin = strings.NewReader(string(raw))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, fail
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return 0, fail
	}
	if err := cmd.Start(); err != nil {
		return 0, fail
	}
	var mu sync.Mutex
	var streamErr error
	size := 0
	var readers sync.WaitGroup
	consume := func(stream string, r io.Reader, w io.Writer) {
		defer readers.Done()
		reader := bufio.NewReader(io.LimitReader(r, MaxBytes+1))
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				normalized := b.Text(line, false)
				mu.Lock()
				size += len(line)
				valid := size <= MaxBytes && len(t.Frames) < MaxFrames
				if stream == "stdout" {
					v, e := object([]byte(strings.TrimSpace(normalized)))
					valid = valid && e == nil && safeCLIJSON([]byte(strings.TrimSpace(normalized))) == nil
					if e == nil && v["type"] == "system" && v["subtype"] == "init" {
						id, ok := v["session_id"].(string)
						if !ok || !poolUUID.MatchString(id) || t.SessionID != "" {
							valid = false
						} else {
							t.SessionID = id
						}
					}
				} else {
					valid = valid && Safe([]byte(normalized)) == nil
				}
				if !valid {
					streamErr = fail
					mu.Unlock()
					cancel()
					return
				}
				t.Frames = append(t.Frames, CLIFrame{Stream: stream, Data: normalized})
				mu.Unlock()
				if _, e := io.WriteString(w, line); e != nil {
					mu.Lock()
					streamErr = fail
					mu.Unlock()
					cancel()
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					mu.Lock()
					streamErr = fail
					mu.Unlock()
					cancel()
				}
				return
			}
		}
	}
	readers.Add(2)
	go consume("stdout", stdout, output)
	go consume("stderr", stderr, diagnostic)
	readers.Wait()
	waitErr := cmd.Wait()
	if streamErr != nil || ctx.Err() != nil {
		return 0, fail
	}
	if waitErr != nil {
		var exit *exec.ExitError
		if !errors.As(waitErr, &exit) || exit.ExitCode() < 0 {
			return 0, fail
		}
		t.ExitCode = exit.ExitCode()
	}
	if t.SessionID != "" {
		path, err := cliSessionPath(c, t.SessionID)
		if err != nil {
			return 0, err
		}
		info, err := os.Lstat(path)
		if err == nil {
			if !info.Mode().IsRegular() || info.Size() > MaxBytes {
				return 0, fail
			}
			f, err := os.Open(path)
			if err != nil {
				return 0, fail
			}
			opened, statErr := f.Stat()
			data, readErr := io.ReadAll(io.LimitReader(f, MaxBytes+1))
			closeErr := f.Close()
			if statErr != nil || !os.SameFile(info, opened) || readErr != nil || closeErr != nil || len(data) > MaxBytes {
				return 0, fail
			}
			t.Snapshot = CLISnapshot{Exists: true, Mode: uint32(info.Mode().Perm()), Data: b.Text(string(data), false)}
		} else if !os.IsNotExist(err) {
			return 0, fail
		}
	}
	if err := t.Save(c.Path); err != nil {
		return 0, err
	}
	return t.ExitCode, nil
}
func cliSessionPath(c CLIProcessConfig, id string) (string, error) {
	if !poolUUID.MatchString(id) {
		return "", errors.New("invalid native CLI session")
	}
	path := filepath.Clean(c.SnapshotPath(id))
	rel, err := filepath.Rel(c.Home, path)
	if err != nil || !filepath.IsAbs(path) || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("CLI session path escaped its private HOME")
	}
	return path, nil
}
