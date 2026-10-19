package claudetape

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type poolLineWriter struct{ lines chan []byte }

func (w poolLineWriter) Write(raw []byte) (int, error) {
	w.lines <- append([]byte(nil), raw...)
	return len(raw), nil
}

func TestPoolProcessStrictReplayAndSeal(t *testing.T) {
	for _, name := range []string{"complete", "changed-request", "unconsumed", "extra-input", "changed-input-file", "wrong-workdir", "wrong-home"} {
		t.Run(name, func(t *testing.T) {
			home, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			work := filepath.Join(home, "work")
			if err := os.Mkdir(work, 0700); err != nil {
				t.Fatal(err)
			}
			t.Chdir(work)
			settings := filepath.Join(home, "settings.json")
			if err := os.WriteFile(settings, []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			const session = "12340000-0000-4000-8000-000000000001"
			args := []string{"--input-format", "stream-json", "--session-id", session, "--settings", settings}
			binding, err := NewPoolBinding(args, home, work)
			if err != nil {
				t.Fatal(err)
			}
			files, err := binding.Files()
			if err != nil {
				t.Fatal(err)
			}
			tape := rigTape()
			tape.Scenario = "pool-model-change"
			tape.Args = binding.Args()
			tape.Files = files
			initialize := json.RawMessage(`{"type":"control_request","request_id":"1","request":{"subtype":"initialize"}}`)
			user := json.RawMessage(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"literal"}]}}`)
			tape.Frames = []Frame{{Direction: "stdin", Data: initialize}, {Direction: "stdout", Data: json.RawMessage(`{"type":"system","subtype":"init","session_id":"${POOL_SESSION}","cwd":"${POOL_CWD}","unknown":true}`)}, {Direction: "stdin", Data: user}, {Direction: "stdout", Data: json.RawMessage(`{"type":"result","session_id":"${POOL_SESSION}"}`)}}
			path := filepath.Join(home, "tape.json")
			if err := tape.Save(path); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(home, "provider-ran")
			provider := filepath.Join(home, "provider")
			if err := os.WriteFile(provider, []byte("#!/bin/sh\ntouch '"+sentinel+"'\nexit 88\n"), 0700); err != nil {
				t.Fatal(err)
			}
			c := PoolProcessConfig{Mode: "replay", Scenario: tape.Scenario, Path: path, Home: home, WorkDir: work, Claude: provider, Claim: filepath.Join(home, "claim"), Finish: filepath.Join(home, "finish")}
			input, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer writer.Close()
			if name == "changed-input-file" {
				if err := os.WriteFile(settings, []byte(`{"extra":true}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "wrong-workdir" {
				c.WorkDir = home
			}
			if name == "wrong-home" {
				t.Setenv("HOME", filepath.Dir(home))
			}
			output := poolLineWriter{lines: make(chan []byte, 4)}
			done := make(chan error, 1)
			go func() { _, err := RunPoolProcess(c, args, input, output, io.Discard); done <- err }()
			write := func(raw []byte) {
				t.Helper()
				if _, err := writer.Write(append(append([]byte(nil), raw...), '\n')); err != nil {
					t.Fatal(err)
				}
			}
			read := func() []byte {
				t.Helper()
				select {
				case raw := <-output.lines:
					return raw
				case err := <-done:
					t.Fatalf("helper ended before native delivery: %v", err)
				case <-time.After(2 * time.Second):
					t.Fatal("native prefix did not arrive")
				}
				return nil
			}
			if name != "changed-input-file" && name != "wrong-workdir" && name != "wrong-home" {
				// A fresh native control ID may bind, while all complete payload fields stay exact.
				write([]byte(strings.ReplaceAll(string(initialize), `"request_id":"1"`, `"request_id":"fresh"`)))
				raw := read()
				if !strings.Contains(string(raw), session) || !strings.Contains(string(raw), work) || !strings.Contains(string(raw), `"unknown":true`) {
					t.Fatal("native metadata restoration changed other fields")
				}
				switch name {
				case "complete", "extra-input":
					write(user)
					read()
					if name == "extra-input" {
						write(user)
					}
				case "changed-request":
					write([]byte(strings.ReplaceAll(string(user), "literal", "changed")))
				}
			}
			if err := os.WriteFile(c.Finish, nil, 0600); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if (err == nil) != (name == "complete") {
					t.Fatalf("wrong complete contract acceptance: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("pool helper did not seal")
			}
			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				t.Fatal("replay executed a real provider")
			}
		})
	}
}
