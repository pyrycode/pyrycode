package claudetape

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLIProcessExactRequestEOFAndTranscript(t *testing.T) {
	for _, kind := range []string{"complete", "changed-input", "changed-file", "extra-input", "escaped-snapshot"} {
		t.Run(kind, func(t *testing.T) {
			home, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Chdir(home)
			meta := filepath.Join(home, CLIMetadataDirectory)
			if os.Mkdir(meta, 0700) != nil {
				t.Fatal("metadata")
			}
			for name, text := range map[string]string{"settings.json": "{}", "system.txt": "original system", "prompt.txt": "ä ö å — 🐍"} {
				if os.WriteFile(filepath.Join(home, name), []byte(text), 0600) != nil {
					t.Fatal("input")
				}
			}
			tape := cliControl()
			args := append([]string(nil), tape.Args...)
			for i, arg := range args {
				if arg == "--settings" {
					args[i+1] = filepath.Join(home, "settings.json")
				}
				if arg == "--append-system-prompt-file" {
					args[i+1] = filepath.Join(home, "system.txt")
				}
			}
			b, err := NewCLIBinding(args, home, home)
			if err != nil {
				t.Fatal(err)
			}
			tape.Files, err = b.Files()
			if err != nil {
				t.Fatal(err)
			}
			tape.ExitCode = 17
			tape.Frames = append(tape.Frames, CLIFrame{Stream: "stderr", Data: "native diagnostic\n"})
			path := filepath.Join(meta, "tape.json")
			if err := tape.Save(path); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(meta, "provider-ran")
			provider := filepath.Join(meta, "provider")
			if os.WriteFile(provider, []byte("#!/bin/sh\ntouch '"+sentinel+"'\nexit 99\n"), 0700) != nil {
				t.Fatal("sentinel")
			}
			input := tape.Input
			switch kind {
			case "changed-input":
				input = `{"type":"user","message":{"content":"changed"}}`
			case "changed-file":
				_ = os.WriteFile(filepath.Join(home, "system.txt"), []byte("changed"), 0600)
			case "extra-input":
				input += tape.Input
			}
			snapshot := filepath.Join(home, ".claude", tape.SessionID+".jsonl")
			if kind == "escaped-snapshot" {
				snapshot = filepath.Join(filepath.Dir(home), "escaped.jsonl")
			}
			cfg := CLIProcessConfig{Mode: "replay", Scenario: tape.Scenario, Path: path, Home: home, Claude: provider, Claim: filepath.Join(meta, "claim"), SnapshotPath: func(string) string { return snapshot }}
			var output, diagnostic bytes.Buffer
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			type result struct {
				code int
				err  error
			}
			done := make(chan result, 1)
			go func() { code, err := RunCLIProcess(cfg, args, reader, &output, &diagnostic); done <- result{code, err} }()
			if _, err := io.WriteString(writer, input); err != nil {
				t.Fatal(err)
			}
			// A complete JSON request without EOF must not release recorded output.
			select {
			case r := <-done:
				t.Fatalf("returned before actual EOF: %v", r.err)
			case <-time.After(30 * time.Millisecond):
			}
			_ = writer.Close()
			var got result
			select {
			case got = <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("replay did not finish")
			}
			if kind != "complete" {
				if got.err == nil {
					t.Fatal("changed request/files/path accepted")
				}
				return
			}
			if got.err != nil || got.code != 17 {
				t.Fatalf("native exit lost: %d %v", got.code, got.err)
			}
			var want strings.Builder
			for _, f := range tape.Frames {
				if f.Stream == "stdout" {
					want.WriteString(b.Text(f.Data, true))
				}
			}
			data, err := os.ReadFile(snapshot)
			if err != nil || string(data) != b.Text(tape.Snapshot.Data, true) || output.String() != want.String() || diagnostic.String() != "native diagnostic\n" {
				t.Fatal("recorded streams or raw transcript encoding changed")
			}
			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				t.Fatal("replay invoked provider")
			}
			if _, err := RunCLIProcess(cfg, args, strings.NewReader(input), io.Discard, io.Discard); err == nil {
				t.Fatal("second native invocation accepted")
			}
		})
	}
}
