package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/config"
)

func testManagedMemory() config.MemorySettings {
	return config.MemorySettings{Vault: config.MemoryVault{Mode: "default"}, Embedding: config.MemoryEmbedding{Provider: "local", Model: "embed"}, Capture: config.MemoryCapture{Agent: "codex", Model: "capture"}}
}
func testManagedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testMemoryMust(t, os.Chmod(home, 0700))
	t.Setenv("HOME", home)
	return home
}
func testManagedCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runMemoryConfiguration(context.Background(), args, &out)
	return out.String(), err
}
func testManagedArgs(vault string) []string {
	return []string{"configure", "--vault", vault, "--embedding-provider", "local", "--embedding-model", "embed", "--capture-agent", "codex", "--capture-model", "capture"}
}
func TestMemoryConfigurationReplacement(t *testing.T) {
	home := testManagedHome(t)
	vault := filepath.Join(home, "vault")
	testMemoryMust(t, os.Mkdir(vault, 0700))
	instructions := filepath.Join(vault, "CLAUDE.md")
	testMemoryMust(t, os.WriteFile(instructions, []byte("keep my instructions\n"), 0600))
	path := filepath.Join(home, ".pyry", "config.json")
	testMemoryMust(t, os.MkdirAll(filepath.Dir(path), 0700))
	original := []byte(`{"relay_url":"wss://unchanged","unknown":9007199254740993,"memory":{"future":true,"vault":{"custom":"stay"},"embedding":{"custom":7},"capture":{"custom":8}}}`)
	testMemoryMust(t, os.WriteFile(path, original, 0600))
	s := testManagedMemory()
	s.Vault = config.MemoryVault{Mode: "separate", Path: vault}
	s.AdditionalRoots = []string{vault}
	testMemoryMust(t, configureMemory(context.Background(), s))
	status, err := memoryStatus(context.Background())
	testMemoryMust(t, err)
	testMemoryCheck(t, status.Configured && status.Vault == s.Vault && reflect.DeepEqual(status.AdditionalRoots, []string{vault}), "saved status mismatch")
	before, err := os.ReadFile(path)
	testMemoryMust(t, err)
	s.Capture.Agent = "unsupported"
	testMemoryReject(t, configureMemory(context.Background(), s))
	after, err := os.ReadFile(path)
	testMemoryMust(t, err)
	testMemoryCheck(t, bytes.Equal(before, after), "invalid configure changed bytes")
	out, err := testManagedCommand(t, testManagedArgs("default")...)
	testMemoryMust(t, err)
	testMemoryCheck(t, out == "{\"configured\":true}\n", "configure output")
	saved, err := config.Load(path)
	testMemoryMust(t, err)
	testMemoryCheck(t, reflect.DeepEqual(*saved.Memory, testManagedMemory()), "replacement retained old choices")
	after, err = os.ReadFile(path)
	testMemoryMust(t, err)
	for _, part := range []string{`9007199254740993`, `"custom": "stay"`, `"future": true`, `wss://unchanged`} {
		testMemoryCheck(t, bytes.Contains(after, []byte(part)), "unknown/unrelated value lost")
	}
	contents, err := os.ReadFile(instructions)
	testMemoryMust(t, err)
	testMemoryCheck(t, string(contents) == "keep my instructions\n", "instructions changed")
	_, err = os.Stat(filepath.Join(home, ".pyry", "memory"))
	testMemoryCheck(t, os.IsNotExist(err), "created memory storage")
	out, err = testManagedCommand(t, "status")
	testMemoryMust(t, err)
	want := `{"configured":true,"vault":{"mode":"default"},"additional_roots":[],"embedding":{"provider":"local","model":"embed","credential_configured":false},"capture":{"agent":"codex","model":"capture"},"transcript_path":` + "\"" + filepath.Join(home, ".pyry", "memory", "recent-transcripts") + "\"}\n"
	testMemoryCheck(t, out == want, "status JSON shape")
}
func TestMemoryConfigurationFailures(t *testing.T) {
	home := testManagedHome(t)
	path := filepath.Join(home, ".pyry", "config.json")
	testMemoryMust(t, os.Mkdir(filepath.Dir(path), 0700))
	for _, body := range []string{"", "{", "[]", "null", `{"memory":{}}`, `{"memory":{"vault":42}}`, `{"memory":{"embedding":{"credential_reference":true}}}`} {
		testMemoryMust(t, os.WriteFile(path, []byte(body), 0600))
		out, err := testManagedCommand(t, "status")
		testMemoryCheck(t, err != nil && out == "", "bad config reported success")
	}
	for _, body := range []string{`{}`, `{"relay_url":"legacy"}`, `{"memory":null}`} {
		testMemoryMust(t, os.WriteFile(path, []byte(body), 0600))
		out, err := testManagedCommand(t, "status")
		testMemoryMust(t, err)
		testMemoryCheck(t, out == "{\"configured\":false}\n", "legacy status")
	}
	testMemoryMust(t, os.Remove(path))
	out, err := testManagedCommand(t, "status")
	testMemoryMust(t, err)
	testMemoryCheck(t, out == "{\"configured\":false}\n", "absent status")
	testMemoryMust(t, os.WriteFile(path, []byte(`{}`), 0600))
	testMemoryMust(t, os.Chmod(filepath.Dir(path), 0500))
	out, err = testManagedCommand(t, testManagedArgs("default")...)
	testMemoryCheck(t, err != nil && out == "", "failed save printed success")
	b, err := os.ReadFile(path)
	testMemoryMust(t, err)
	testMemoryCheck(t, string(b) == `{}`, "failed save changed bytes")
	testMemoryMust(t, os.Chmod(filepath.Dir(path), 0700))
	for _, badHome := range []string{"", "relative", filepath.Join(home, "absent"), path} {
		t.Setenv("HOME", badHome)
		for _, args := range [][]string{{"status"}, testManagedArgs("default")} {
			out, err := testManagedCommand(t, args...)
			testMemoryCheck(t, err != nil && out == "", "unavailable home admitted")
		}
	}
}
func TestMemoryConfigurationSyntax(t *testing.T) {
	testManagedHome(t)
	valid := testManagedArgs("default")
	cases := [][]string{nil, {"status", "extra"}, {"configure"}, {"bad"}, append(append([]string{}, valid...), "extra"), append(append([]string{}, valid...), "--unknown", memoryOld), append(append([]string{}, valid...), "--credential-reference", memoryOld)}
	for i := 1; i < len(valid); i += 2 {
		cases = append(cases, append(append([]string{}, valid[:i]...), valid[i+2:]...))
	}
	for _, args := range cases {
		out, err := testManagedCommand(t, args...)
		testMemoryCheck(t, err != nil && out == "" && !strings.Contains(err.Error(), memoryOld), "syntax success/leak")
	}
	for _, flag := range []string{"--vault", "--embedding-provider", "--embedding-model", "--capture-agent", "--capture-model", "--knowledge-folder", "--credential-reference"} {
		out, err := testManagedCommand(t, append(append([]string{}, valid...), flag)...)
		testMemoryCheck(t, err != nil && out == "", "missing/duplicate flag accepted")
		testMemoryCheck(t, strings.Contains(helpText, flag), "missing help flag")
	}
	testMemoryCheck(t, strings.Contains(helpText, "pyry memory status") && strings.Contains(helpText, "pyry memory credential"), "help dropped memory commands")
}
func TestMemoryConfigurationCredentials(t *testing.T) {
	home := testManagedHome(t)
	s := testManagedMemory()
	for _, provider := range []string{"local", "openai"} {
		for _, agent := range []string{"claude", "codex"} {
			s.Embedding.Provider = provider
			s.Capture.Agent = agent
			if provider == "openai" {
				s.Embedding.CredentialReference = testMemorySet(t, memoryCredentialStore{home: home}, memoryOld)
			}
			testMemoryMust(t, configureMemory(context.Background(), s))
			out, err := testManagedCommand(t, "status")
			testMemoryMust(t, err)
			for _, secret := range []string{memoryOld, s.Embedding.CredentialReference, "backend", "generation"} {
				if secret != "" {
					testMemoryCheck(t, !strings.Contains(out, secret), "status credential disclosure")
				}
			}
			status, err := memoryStatus(context.Background())
			testMemoryMust(t, err)
			testMemoryCheck(t, status.Embedding.CredentialConfigured == (provider == "openai") && status.Capture.Agent == agent, "independent choices")
		}
	}
	ref := s.Embedding.CredentialReference
	for _, bad := range []string{"", memoryNew, ref + "x"} {
		s.Embedding.CredentialReference = bad
		testMemoryReject(t, configureMemory(context.Background(), s))
	}
	s.Embedding.CredentialReference = ref
	selection, sel := testMemorySelection(t, memoryCredentialStore{home: home})
	testMemoryMust(t, os.Remove(filepath.Join(filepath.Dir(selection), sel.Generation+".token")))
	out, err := testManagedCommand(t, "status")
	testMemoryCheck(t, err != nil && out == "" && !strings.Contains(err.Error(), ref), "status cached credential")
	testMemoryReject(t, configureMemory(context.Background(), s))
	testMemoryMust(t, os.WriteFile(filepath.Join(filepath.Dir(selection), sel.Generation+".token"), []byte(memoryOld), 0600))
	testMemoryMust(t, os.Chmod(filepath.Dir(selection), 0755))
	out, err = testManagedCommand(t, "status")
	testMemoryCheck(t, err != nil && out == "", "unsafe credential storage admitted")
	testMemoryReject(t, configureMemory(context.Background(), s))
	s = testManagedMemory()
	testMemoryMust(t, configureMemory(context.Background(), s))
	cfg, err := config.Load(filepath.Join(home, ".pyry", "config.json"))
	testMemoryMust(t, err)
	testMemoryCheck(t, cfg.Memory.Embedding.CredentialReference == "", "local retained reference")
	_, err = memoryStatus(context.Background())
	testMemoryMust(t, err)
}
func TestMemoryConfigurationProcess(t *testing.T) {
	if os.Getenv("PYRY_MEMORY_CONFIG_HELPER") == "1" {
		args := os.Args
		for i, arg := range args {
			if arg == "--" {
				if err := runArgs(append([]string{"pyry"}, args[i+1:]...)); err != nil {
					_, _ = os.Stderr.WriteString(err.Error())
					os.Exit(1)
				}
				os.Exit(0)
			}
		}
		os.Exit(2)
	}
	home := testManagedHome(t)
	runChild := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestMemoryConfigurationProcess$", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "PYRY_MEMORY_CONFIG_HELPER=1")
		cmd.Dir = home
		out, err := cmd.CombinedOutput()
		testMemoryMust(t, err)
		return string(out)
	}
	testMemoryCheck(t, runChild(append([]string{"memory"}, testManagedArgs("default")...)...) == "{\"configured\":true}\n", "process configure")
	out := runChild("memory", "status")
	var status memoryConfigurationStatus
	testMemoryMust(t, json.Unmarshal([]byte(out), &status))
	testMemoryCheck(t, status.Configured && status.Vault.Mode == "default" && status.Vault.Path == "", "process persisted status")
	testMemoryCheck(t, strings.Contains(runChild("help"), "pyry memory configure"), "process help")
	for _, args := range [][]string{{"memory", "status", "extra"}, {"memory", "configure", "--credential-reference", memoryOld}} {
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestMemoryConfigurationProcess$", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "PYRY_MEMORY_CONFIG_HELPER=1")
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		testMemoryCheck(t, err != nil && stdout.Len() == 0 && !strings.Contains(stderr.String(), memoryOld), "process rejection/output")
	}
}
func TestEffectiveMemoryRoots(t *testing.T) {
	home := testManagedHome(t)
	vault := filepath.Join(home, "notes")
	child := filepath.Join(vault, "nested")
	sibling := filepath.Join(home, "notes-old")
	for _, dir := range []string{child, sibling} {
		testMemoryMust(t, os.MkdirAll(dir, 0700))
	}
	s := testManagedMemory()
	s.AdditionalRoots = []string{child, vault, child, sibling}
	testMemoryMust(t, configureMemory(context.Background(), s))
	before, err := os.ReadFile(filepath.Join(home, ".pyry", "config.json"))
	testMemoryMust(t, err)
	transcript := filepath.Join(home, ".pyry", "memory", "recent-transcripts")
	for _, cwd := range []string{vault, sibling} {
		base := resolveStartupWorkspaceBase(func() (string, error) { return cwd, nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
		got, err := resolveEffectiveMemory(context.Background(), s, base)
		testMemoryMust(t, err)
		search := []string{vault, sibling, transcript}
		if cwd == sibling {
			search = []string{sibling, vault, transcript}
		}
		testMemoryCheck(t, got.VaultPath == cwd && got.TranscriptPath == transcript && reflect.DeepEqual(got.AdditionalRoots, []string{vault, sibling}) && reflect.DeepEqual(got.SearchRoots, search), "effective root identities/union")
	}
	s.Vault = config.MemoryVault{Mode: "separate", Path: vault}
	got, err := resolveEffectiveMemory(context.Background(), s, sibling)
	testMemoryMust(t, err)
	testMemoryCheck(t, got.VaultPath == vault, "separate used startup base")
	s = testManagedMemory()
	testMemoryMust(t, os.MkdirAll(transcript, 0700))
	s.AdditionalRoots = []string{filepath.Dir(transcript)}
	got, err = resolveEffectiveMemory(context.Background(), s, vault)
	testMemoryCheck(t, err != nil && reflect.DeepEqual(got, effectiveMemory{}), "credential parent root admitted")
	s.AdditionalRoots = []string{transcript}
	got, err = resolveEffectiveMemory(context.Background(), s, vault)
	testMemoryMust(t, err)
	testMemoryCheck(t, reflect.DeepEqual(got.SearchRoots, []string{vault, transcript}), "transcript indexed twice")
	for _, base := range []string{home, transcript, filepath.Join(home, "absent"), "relative", ""} {
		got, err = resolveEffectiveMemory(context.Background(), s, base)
		testMemoryCheck(t, err != nil && reflect.DeepEqual(got, effectiveMemory{}), "unsafe default resolution admitted")
	}
	s.AdditionalRoots = []string{vault}
	nested := filepath.Join(vault, "nested")
	got, err = resolveEffectiveMemory(context.Background(), s, nested)
	testMemoryMust(t, err)
	testMemoryCheck(t, got.VaultPath == nested && reflect.DeepEqual(got.SearchRoots, []string{vault, transcript}), "parent search root lost vault destination")
	after, err := os.ReadFile(filepath.Join(home, ".pyry", "config.json"))
	testMemoryMust(t, err)
	testMemoryCheck(t, bytes.Equal(before, after), "effective resolution changed settings")
}
