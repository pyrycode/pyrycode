package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/streamsup"
)

// plantedRef has a space so a split argument would be caught.
const plantedRef = "op://Private/acct item 2825/credential"

// writeFakeOp writes a stand-in 1Password CLI at dir/name. It accepts only
// exactly "read <plantedRef>" and otherwise runs body.
func writeFakeOp(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\n" +
		"[ \"$#\" -eq 2 ] && [ \"$1\" = read ] && [ \"$2\" = '" + plantedRef + "' ] || { echo bad-args; exit 7; }\n" +
		body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// leakBody prints the planted token and reference on both streams, as a
// failing CLI might.
const leakBody = "printf '%s %s\\n' \"$2\" " + plantedToken + "; printf '%s %s\\n' \"$2\" " + plantedToken + " >&2"

func opAccount(t *testing.T, cli string) (*claudeAccount, *safeLog) {
	t.Helper()
	log := &safeLog{}
	a, err := newClaudeAccount(plantedRef, "", cli, "", t.TempDir(), testLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	if a.provider() == nil || a.status().Kind != "1password" {
		t.Fatalf("op:// source not selected: %+v", a.status())
	}
	return a, log
}

// assertNoOpLeak fails if the planted token or reference reached the log, the
// error or the status.
func assertNoOpLeak(t *testing.T, a *claudeAccount, log *safeLog, err error) {
	t.Helper()
	surfaces := []string{log.String(), fmt.Sprintf("%+v", a.status())}
	if err != nil {
		surfaces = append(surfaces, err.Error())
	}
	for _, s := range surfaces {
		for _, secret := range []string{plantedToken, plantedRef, "acct item 2825"} {
			if strings.Contains(s, secret) {
				t.Fatalf("%q leaks %q", s, secret)
			}
		}
	}
}

func TestReadOpReference(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	writeOwnerToken(t, tokenPath, plantedToken)
	tests := []struct {
		name        string
		body        string
		timeout     time.Duration
		cancelAfter time.Duration
		wantToken   string
		wantFailure streamsup.AccountTokenFailure
		wantReason  string
	}{
		{name: "success", body: "cat '" + tokenPath + "'", wantToken: plantedToken},
		{name: "crlf", body: "printf '%s\\r\\n' " + plantedToken, wantToken: plantedToken},
		{name: "non-zero exit", body: leakBody + "; exit 1", wantFailure: streamsup.AccountTokenReadFailure, wantReason: "1Password read failed"},
		{name: "empty", body: "exit 0", wantFailure: streamsup.AccountTokenEmptyOutput, wantReason: "1Password output empty"},
		{name: "space in output", body: "printf '%s %s\\n' \"$2\" " + plantedToken, wantFailure: streamsup.AccountTokenInvalidOutput, wantReason: "1Password output invalid"},
		{name: "two lines", body: "printf '%s\\n%s\\n' " + plantedToken + " " + plantedToken, wantFailure: streamsup.AccountTokenInvalidOutput, wantReason: "1Password output invalid"},
		{name: "oversized", body: "head -c 5000 /dev/zero | tr '\\0' a", wantFailure: streamsup.AccountTokenInvalidOutput, wantReason: "1Password output invalid"},
		// The forked sleep keeps stdout open after the script itself is gone.
		{name: "hang cut by deadline", body: leakBody + "; sleep 30 & wait", timeout: 200 * time.Millisecond, wantFailure: streamsup.AccountTokenTimeout, wantReason: "1Password read timed out"},
		{name: "hang cut by cancel", body: leakBody + "; sleep 30 & wait", cancelAfter: 200 * time.Millisecond, wantFailure: streamsup.AccountTokenCancellation, wantReason: "1Password read cancelled"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cli := writeFakeOp(t, t.TempDir(), "op", tc.body)
			a, log := opAccount(t, cli)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.timeout > 0 {
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
				defer cancel()
			}
			if tc.cancelAfter > 0 {
				time.AfterFunc(tc.cancelAfter, cancel)
			}
			start := time.Now()
			tok, failure, err := a.provider()(ctx)
			if elapsed := time.Since(start); elapsed > 3*time.Second {
				t.Fatalf("read held for %v", elapsed)
			}
			if tc.wantFailure == "" {
				if err != nil || failure != "" || tok != tc.wantToken || a.status().State != claudeAccountReady {
					t.Fatalf("got (%q, %q, %v) status %+v, want token", tok, failure, err, a.status())
				}
				return
			}
			if tok != "" || err == nil || failure != tc.wantFailure {
				t.Fatalf("got (%q, %q, %v), want refusal %q", tok, failure, err, tc.wantFailure)
			}
			if got := a.status(); got.State != claudeAccountFailed || got.Reason != tc.wantReason {
				t.Fatalf("status = %+v, want reason %q", got, tc.wantReason)
			}
			assertNoOpLeak(t, a, log, err)
		})
	}
}

func TestReadOpReference_MissingExecutable(t *testing.T) {
	for _, cli := range []string{"pyry-missing-op-2825", filepath.Join(t.TempDir(), "no such dir", "op")} {
		a, log := opAccount(t, cli)
		tok, failure, err := a.provider()(context.Background())
		if tok != "" || err == nil || failure != streamsup.AccountTokenReadFailure {
			t.Fatalf("%s: got (%q, %q, %v)", cli, tok, failure, err)
		}
		if got := a.status(); got.Reason != "1Password CLI unavailable" {
			t.Fatalf("%s: status = %+v", cli, got)
		}
		assertNoOpLeak(t, a, log, err)
		if strings.Contains(log.String()+err.Error(), cli) {
			t.Fatalf("%s: CLI path leaked", cli)
		}
	}
}

// op.exe, as reached through WSL interop, is found by bare name on a PATH
// entry with a space and by its absolute path.
func TestReadOpReference_OpExeOnPathWithSpace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Program Files", "1Password CLI")
	abs := writeFakeOp(t, dir, "op.exe", "printf '%s\\n' "+plantedToken)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, cli := range []string{"op.exe", abs} {
		a, _ := opAccount(t, cli)
		if tok, _, err := a.provider()(context.Background()); err != nil || tok != plantedToken {
			t.Fatalf("%s: got (%q, %v)", cli, tok, err)
		}
	}
}

// Each read runs the CLI afresh: a failure keeps no older token, a later
// success clears it, and a rotated item is read at the next call.
func TestReadOpReference_RecoveryAndRotation(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	cli := writeFakeOp(t, t.TempDir(), "op", "cat '"+tokenPath+"' 2>/dev/null || { "+leakBody+"; exit 1; }")
	a, log := opAccount(t, cli)
	a.prime(context.Background())
	if got := a.status(); got.State != claudeAccountFailed || got.Reason != "1Password read failed" {
		t.Fatalf("after prime: %+v", got)
	}
	writeOwnerToken(t, tokenPath, plantedToken)
	if tok, _, err := a.provider()(context.Background()); err != nil || tok != plantedToken {
		t.Fatalf("recovered read: (%q, %v)", tok, err)
	}
	if got := a.status(); got.State != claudeAccountReady || got.Reason != "" {
		t.Fatalf("after recovery: %+v", got)
	}
	const rotated = "sk-ant-oat01-rotated-2825"
	writeOwnerToken(t, tokenPath, rotated)
	if tok, _, err := a.provider()(context.Background()); err != nil || tok != rotated {
		t.Fatalf("rotated read: (%q, %v)", tok, err)
	}
	if err := os.Remove(tokenPath); err != nil {
		t.Fatal(err)
	}
	if tok, _, err := a.provider()(context.Background()); tok != "" || err == nil {
		t.Fatalf("removed item: (%q, %v)", tok, err)
	}
	if !strings.Contains(log.String(), "recovered") {
		t.Fatalf("recovery not logged:\n%s", log.String())
	}
	assertNoOpLeak(t, a, log, nil)
}

func TestResolveClaudeAccountOpCLI(t *testing.T) {
	tests := []struct {
		name       string
		flag, env  string
		file       string
		wantCLI    string
		wantOrigin string
		wantErr    bool
	}{
		{name: "default", wantCLI: "op", wantOrigin: "default"},
		{name: "file without op_cli", file: `{"source":"/c"}`, wantCLI: "op", wantOrigin: "default"},
		{name: "flag wins", flag: "op.exe", env: "/env/op", file: `{"op_cli":"/file/op"}`, wantCLI: "op.exe", wantOrigin: "flag"},
		{name: "env wins over file", env: "/env/op", file: `{"op_cli":"/file/op"}`, wantCLI: "/env/op", wantOrigin: "env"},
		{name: "file used last", file: `{"op_cli":"/mnt/c/Program Files/1Password CLI/op.exe"}`, wantCLI: "/mnt/c/Program Files/1Password CLI/op.exe", wantOrigin: "file"},
		{name: "non-string op_cli", file: `{"op_cli":["op"]}`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.file != "" {
				writeAccountFile(t, dir, tc.file, 0o600)
			}
			cli, origin, err := resolveClaudeAccountOpCLI(tc.flag, tc.env, dir)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "file") {
					t.Fatalf("err = %v, want file refusal", err)
				}
				return
			}
			if err != nil || cli != tc.wantCLI || !strings.Contains(origin, tc.wantOrigin) {
				t.Fatalf("got (%q, %q, %v), want (%q, %q)", cli, origin, err, tc.wantCLI, tc.wantOrigin)
			}
		})
	}
}

// The CLI precedence is independent of where the source came from: a flag
// source still runs the CLI named in claude-account.json.
func TestNewClaudeAccount_FlagSourceHonoursFileOpCLI(t *testing.T) {
	dir := t.TempDir()
	cli := writeFakeOp(t, t.TempDir(), "my op", "printf '%s\\n' "+plantedToken)
	writeAccountFile(t, dir, `{"op_cli":"`+cli+`"}`, 0o600)
	a, err := newClaudeAccount(plantedRef, "", "", "", dir, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if tok, _, err := a.provider()(context.Background()); err != nil || tok != plantedToken {
		t.Fatalf("got (%q, %v)", tok, err)
	}
}

func TestNewClaudeAccount_RefusesOpCLIWithoutEchoingValue(t *testing.T) {
	for _, value := range []string{"op read " + plantedToken, "./op", "bin/op", "op;" + plantedToken, "..", "/usr/bin/op\n" + plantedToken} {
		dir := t.TempDir()
		for _, sel := range []struct{ flag, env, file, origin string }{
			{flag: value, origin: "flag -" + claudeAccountOpCLIFlagName},
			{env: value, origin: "env " + claudeAccountOpCLIEnv},
			{file: `{"op_cli":` + fmt.Sprintf("%q", value) + `}`, origin: "file " + filepath.Join(dir, claudeAccountFileName)},
		} {
			if sel.file != "" {
				writeAccountFile(t, dir, sel.file, 0o600)
			}
			_, err := newClaudeAccount(plantedRef, "", sel.flag, sel.env, dir, quietLogger())
			if err == nil {
				t.Fatalf("op CLI %q from %s accepted", value, sel.origin)
			}
			if !strings.Contains(err.Error(), sel.origin) || strings.Contains(err.Error(), value) || strings.Contains(err.Error(), plantedRef) {
				t.Fatalf("error %q: want origin %q and no value", err, sel.origin)
			}
		}
	}
}

// A file source never consults the CLI setting, so an unusable one there
// changes nothing.
func TestNewClaudeAccount_FileSourceIgnoresOpCLI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeOwnerToken(t, path, plantedToken)
	a, err := newClaudeAccount(path, "", "op read x", "", t.TempDir(), quietLogger())
	if err != nil || a.status().Kind != "file" {
		t.Fatalf("file source refused: %v", err)
	}
}

// Two instances under one HOME, one reading a token file and one reading
// 1Password through a fake op on PATH, each hand their Claude child exactly
// their own token.
func TestClaudeAccount_FileAndOnePasswordInstancesEachGetOwnToken(t *testing.T) {
	home := accountTestHome(t)
	t.Setenv(claudeAccountOpCLIEnv, "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "inherited-conflicting-token-2825")

	a := &accountInstance{name: "acct-file", token: "planted-token-file-2825", tokenPath: filepath.Join(home, "secrets-a", "token")}
	b := &accountInstance{name: "acct-op", token: "planted-token-op-2825"}
	writeOwnerToken(t, a.tokenPath, a.token)
	// The fake op and the 1Password item it serves live outside HOME.
	opDir := t.TempDir()
	b.tokenPath = filepath.Join(opDir, "item")
	writeOwnerToken(t, b.tokenPath, b.token)
	writeFakeOp(t, opDir, "op", "cat '"+b.tokenPath+"'")
	t.Setenv("PATH", opDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	instanceB := resolveInstanceDirPath(b.name)
	if err := os.MkdirAll(instanceB, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(instanceB, claudeAccountFileName), []byte(`{"source":"`+plantedRef+`"}`), 0o600); err != nil {
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
		for {
			seen, err := os.ReadFile(inst.seenPath)
			if err == nil {
				if string(seen) != inst.token {
					t.Errorf("%s: child saw %q, want its own planted token", inst.name, seen)
				}
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("%s: claude helper never launched", inst.name)
			case <-time.After(20 * time.Millisecond):
			}
		}
		logs, err := control.Logs(ctx, inst.socket)
		if err != nil {
			t.Fatalf("%s: logs: %v", inst.name, err)
		}
		joined := strings.Join(logs.Lines, "\n")
		for _, secret := range []string{a.token, b.token, plantedRef} {
			if strings.Contains(joined, secret) {
				t.Errorf("%s: log ring holds %q", inst.name, secret)
			}
		}
	}
}
