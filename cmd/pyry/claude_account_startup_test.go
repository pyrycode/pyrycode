package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
)

// accountInstance is one in-process daemon of the two-instance startup test.
type accountInstance struct {
	name, socket, token, tokenPath, seenPath string
	done                                     chan error
}

// writeTokenHelper writes a stand-in claude that records the account token it
// was launched with and then holds its stdin open like a live child.
func writeTokenHelper(t *testing.T, dir, seenPath string) string {
	t.Helper()
	path := filepath.Join(dir, "claude-helper.sh")
	script := "#!/bin/sh\nprintf '%s' \"$CLAUDE_CODE_OAUTH_TOKEN\" > '" + seenPath + ".tmp' && mv '" + seenPath + ".tmp' '" + seenPath + "'\nexec cat >/dev/null\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func startAccountInstance(t *testing.T, home string, inst *accountInstance, extra ...string) {
	t.Helper()
	dir := filepath.Join(home, inst.name)
	workdir := filepath.Join(dir, "work")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	inst.socket = filepath.Join(home, inst.name+".sock")
	inst.seenPath = filepath.Join(dir, "seen-token")
	helper := writeTokenHelper(t, dir, inst.seenPath)
	args := append([]string{"-pyry-name", inst.name, "-pyry-socket", inst.socket, "-pyry-workdir", workdir, "-pyry-codex", "/bin/true", "-pyry-claude", helper}, extra...)
	inst.done = make(chan error, 1)
	go func() { inst.done <- runSupervisor(args) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = control.Stop(ctx, inst.socket)
		select {
		case <-inst.done:
		case <-ctx.Done():
			t.Errorf("%s: daemon did not stop", inst.name)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		if _, err := control.SessionsList(ctx, inst.socket); err == nil {
			return
		}
		select {
		case err := <-inst.done:
			inst.done <- err
			t.Fatalf("%s: startup: %v", inst.name, err)
		case <-ctx.Done():
			t.Fatalf("%s: daemon never became ready", inst.name)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func writeOwnerToken(t *testing.T, path, token string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func accountTestHome(t *testing.T) string {
	t.Helper()
	home := shortTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("PYRY_RELAY_URL", "")
	t.Setenv(claudeAccountSourceEnv, "")
	if err := os.MkdirAll(filepath.Dir(resolveConfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolveConfigPath(), []byte(`{"relay_url":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// Two instances under one HOME each hand their own Claude child only their own
// token, over an inherited conflicting one, and neither token reaches a file
// the daemons write or their log rings.
func TestClaudeAccount_TwoInstancesEachGetOwnToken(t *testing.T) {
	home := accountTestHome(t)
	const inherited = "inherited-conflicting-token-2824"
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", inherited)

	a := &accountInstance{name: "acct-a", token: "planted-token-alpha-2824", tokenPath: filepath.Join(home, "secrets-a", "token")}
	b := &accountInstance{name: "acct-b", token: "planted-token-bravo-2824", tokenPath: filepath.Join(home, "secrets-b", "token")}
	writeOwnerToken(t, a.tokenPath, a.token)
	writeOwnerToken(t, b.tokenPath, b.token)
	// B selects its source through its instance file, A through the flag.
	instanceB := resolveInstanceDirPath(b.name)
	if err := os.MkdirAll(instanceB, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(instanceB, claudeAccountFileName), []byte(`{"source":"`+b.tokenPath+`","op_cli":"ignored"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	startAccountInstance(t, home, a, "-"+claudeAccountFlagName, a.tokenPath)
	startAccountInstance(t, home, b)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, inst := range []*accountInstance{a, b} {
		if _, err := control.SessionsNew(ctx, inst.socket, "account"); err != nil {
			t.Fatalf("%s: sessions.new: %v", inst.name, err)
		}
	}
	for _, inst := range []*accountInstance{a, b} {
		var seen []byte
		for {
			var err error
			if seen, err = os.ReadFile(inst.seenPath); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("%s: claude helper never launched", inst.name)
			case <-time.After(20 * time.Millisecond):
			}
		}
		if string(seen) != inst.token {
			t.Errorf("%s: child saw %q, want its own planted token", inst.name, seen)
		}
	}

	secrets := map[string]bool{}
	for _, inst := range []*accountInstance{a, b} {
		secrets[inst.tokenPath] = true
		secrets[inst.seenPath] = true
		logs, err := control.Logs(ctx, inst.socket)
		if err != nil {
			t.Fatalf("%s: logs: %v", inst.name, err)
		}
		joined := strings.Join(logs.Lines, "\n")
		for _, tok := range []string{a.token, b.token} {
			if strings.Contains(joined, tok) {
				t.Errorf("%s: log ring holds a planted token", inst.name)
			}
		}
	}
	err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || secrets[path] || !d.Type().IsRegular() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		for _, tok := range []string{a.token, b.token} {
			if strings.Contains(string(data), tok) {
				t.Errorf("planted token written to %s", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// An unusable source stops startup with an error that names where the setting
// came from and never repeats its value.
func TestClaudeAccount_RefusedSourceStopsStartup(t *testing.T) {
	const pasted = "sk-ant-oat01-pasted-by-mistake"
	tests := []struct {
		name   string
		flag   string
		env    string
		file   string
		origin string
	}{
		{name: "flag relative", flag: pasted, origin: "flag"},
		{name: "env scheme", env: "op://vault/" + pasted, origin: "env"},
		{name: "file non-string", file: `{"source":["` + pasted + `"]}`, origin: "file"},
		{name: "file invalid json", file: `{"source":"` + pasted, origin: "file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := accountTestHome(t)
			t.Setenv(claudeAccountSourceEnv, tc.env)
			if tc.file != "" {
				dir := resolveInstanceDirPath("acct-refused")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, claudeAccountFileName), []byte(tc.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			socket := filepath.Join(home, "refused.sock")
			args := []string{"-pyry-name", "acct-refused", "-pyry-socket", socket, "-pyry-workdir", home, "-pyry-codex", "/bin/true", "-pyry-claude", "/bin/true"}
			if tc.flag != "" {
				args = append(args, "-"+claudeAccountFlagName+"="+tc.flag)
			}
			err := runSupervisor(args)
			if err == nil {
				t.Fatal("startup accepted an unusable source")
			}
			if !strings.Contains(err.Error(), tc.origin) || strings.Contains(err.Error(), pasted) {
				t.Fatalf("error %q: want origin %q and no value", err, tc.origin)
			}
			if _, statErr := os.Stat(socket); statErr == nil {
				t.Fatal("control socket opened despite refused source")
			}
		})
	}
}

// A failed startup read leaves the control socket serving and Claude launch
// refused; once the token file appears, the next launch attempt reads it.
func TestClaudeAccount_FailedReadKeepsDaemonUpAndRecovers(t *testing.T) {
	home := accountTestHome(t)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "inherited-conflicting-token-2824")
	inst := &accountInstance{name: "acct-late", token: "planted-token-late-2824", tokenPath: filepath.Join(home, "secrets", "token")}
	startAccountInstance(t, home, inst, "-"+claudeAccountFlagName+"="+inst.tokenPath)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := control.SessionsNew(ctx, inst.socket, "late"); err != nil {
		t.Fatalf("sessions.new: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(inst.seenPath); err == nil {
		t.Fatal("claude launched without a readable token")
	}
	if _, err := control.SessionsList(ctx, inst.socket); err != nil {
		t.Fatalf("control socket stopped serving: %v", err)
	}
	writeOwnerToken(t, inst.tokenPath, inst.token)
	for {
		if seen, err := os.ReadFile(inst.seenPath); err == nil {
			if string(seen) != inst.token {
				t.Fatalf("child saw %q, want the recovered token", seen)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("claude never launched after the token appeared")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
