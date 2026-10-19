//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

func TestClaudeTapeProcessContract(t *testing.T) {
	for _, variant := range []string{"complete", "changed-settings", "wrong-request", "incomplete", "extra-request"} {
		t.Run(variant, func(t *testing.T) {
			home := t.TempDir()
			c := tapeProcessConfig{Mode: "replay", Home: home, Daemon: filepath.Join(home, "pyry"), Socket: filepath.Join(home, "socket"), Scenario: "unit-process", Path: filepath.Join(home, "fixture.json"), Finish: filepath.Join(home, "finish"), Receipt: filepath.Join(home, "receipt"), Claim: filepath.Join(home, "claim")}
			forbidden := filepath.Join(home, "forbidden-launch")
			c.Claude = filepath.Join(home, "must-not-launch")
			if err := os.WriteFile(c.Claude, []byte("#!/bin/sh\ntouch '"+forbidden+"'\nexit 99\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			settings := filepath.Join(home, "settings.json")
			system := filepath.Join(home, "system.txt")
			mcp := filepath.Join(home, "mcp.json")
			for path, contents := range map[string]string{settings: `{"effort":"low"}`, system: "Controlled system instruction.", mcp: `{"mcpServers":{}}`} {
				if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"--settings", settings, "--append-system-prompt-file", system, "--mcp-config", mcp}
			files, err := tapeFiles(args, &c)
			if err != nil {
				t.Fatal(err)
			}
			tape := &claudetape.Tape{Version: 1, Scenario: c.Scenario, Producer: claudetape.Producer{Revision: strings.Repeat("a", 40), BinarySHA256: strings.Repeat("b", 64), ClaudeVersion: "synthetic unit-process rig", CapturedAt: "2026-10-19T00:00:00Z"}, Args: tapeArgs(args, c), Files: files, Frames: []claudetape.Frame{
				{Direction: "stdin", Data: json.RawMessage(`{"type":"user","message":{"content":"hello"}}`)},
				{Direction: "stdout", Data: json.RawMessage(`{"type":"assistant","message":{"content":"reply"}}`)},
				{Direction: "stdin", Data: json.RawMessage(`{"type":"user","message":{"content":"second"}}`)},
				{Direction: "stdout", Data: json.RawMessage(`{"type":"result","subtype":"success"}`)},
			}}
			if err = tape.Save(c.Path); err != nil {
				t.Fatal(err)
			}
			if variant == "changed-settings" {
				if err = os.WriteFile(settings, []byte(`{"effort":"high"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			wrapper := tapeWrapper(t, c)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, wrapper, args...)
			cmd.WaitDelay = time.Second
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					cancel()
					_ = stdin.Close()
					_ = cmd.Wait()
				}
			}()
			reader := bufio.NewReader(stdout)
			if variant != "changed-settings" {
				first := `{"type":"user","message":{"content":"hello"}}`
				if variant == "wrong-request" {
					first = `{"type":"user","message":{"content":"changed"}}`
				}
				if _, err = stdin.Write([]byte(first + "\n")); err != nil {
					t.Fatal(err)
				}
				if variant != "wrong-request" {
					if _, err = reader.ReadString('\n'); err != nil {
						t.Fatal("recorded output did not reach the pipe")
					}
					if variant != "incomplete" {
						if _, err = stdin.Write([]byte(`{"type":"user","message":{"content":"second"}}` + "\n")); err != nil {
							t.Fatal(err)
						}
						if _, err = reader.ReadString('\n'); err != nil {
							t.Fatal(err)
						}
						if variant == "extra-request" {
							if _, err = stdin.Write([]byte(`{"type":"user","message":{"content":"extra"}}` + "\n")); err != nil {
								t.Fatal(err)
							}
							// EOF witnesses the rejected extra input before the scenario seal.
							if _, err = reader.ReadString('\n'); err == nil {
								t.Fatal("extra request produced a reply")
							}
						}
					}
				}
			}
			if err = os.WriteFile(c.Finish, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			waitErr := cmd.Wait()
			waited = true
			_ = stdin.Close()
			if ctx.Err() != nil {
				t.Fatal("tape helper did not finish")
			}
			raw, err := os.ReadFile(c.Receipt)
			if err != nil {
				t.Fatal(err)
			}
			var receipt tapeReceipt
			if json.Unmarshal(raw, &receipt) != nil {
				t.Fatal("invalid process receipt")
			}
			if variant == "complete" {
				if waitErr != nil || !receipt.Success || receipt.Frames != len(tape.Frames) {
					t.Fatal("complete offline exchange failed")
				}
			} else if waitErr == nil || receipt.Success {
				t.Fatal("incomplete or changed exchange certified success")
			}
			if _, err = os.Stat(forbidden); !os.IsNotExist(err) {
				t.Fatal("offline replay launched Claude")
			}
		})
	}
}
