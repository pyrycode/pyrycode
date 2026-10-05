package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/streamsup"
)

// plantedItem has a space so a split argument would be caught.
const plantedItem = "pyry item 2815"

// keychainPlatforms is each platform's tool and the exact argument vector its
// fake accepts.
var keychainPlatforms = []struct {
	goos, tool string
	args       []string
}{
	{goos: "darwin", tool: "security", args: []string{"find-generic-password", "-s", plantedItem, "-w"}},
	{goos: "linux", tool: "secret-tool", args: []string{"lookup", "service", plantedItem}},
}

func setKeychainGOOS(t *testing.T, goos string) {
	t.Helper()
	old := claudeAccountGOOS
	claudeAccountGOOS = goos
	t.Cleanup(func() { claudeAccountGOOS = old })
}

// writeFakeKeychainTool puts a stand-in tool first on PATH. It accepts only
// exactly args and otherwise exits 7.
func writeFakeKeychainTool(t *testing.T, tool string, args []string, body string) {
	t.Helper()
	dir := t.TempDir()
	check := fmt.Sprintf("[ \"$#\" -eq %d ]", len(args))
	for i, arg := range args {
		check += fmt.Sprintf(" && [ \"$%d\" = '%s' ]", i+1, arg)
	}
	// The planted item name is printed from the script text rather than an
	// argument, so the leak check holds for every vector.
	leak := "printf '%s %s\\n' '" + plantedItem + "' " + plantedToken
	body = strings.ReplaceAll(body, "LEAK", leak+"; "+leak+" >&2")
	script := "#!/bin/sh\n" + check + " || { echo bad-args; exit 7; }\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func keychainAccount(t *testing.T) (*claudeAccount, *safeLog) {
	t.Helper()
	log := &safeLog{}
	a, err := newClaudeAccount("keychain:"+plantedItem, "", "", "", t.TempDir(), testLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	if a.provider() == nil || a.status().Kind != "os_keychain" {
		t.Fatalf("keychain source not selected: %+v", a.status())
	}
	return a, log
}

func assertNoKeychainLeak(t *testing.T, a *claudeAccount, log *safeLog, err error) {
	t.Helper()
	surfaces := []string{log.String(), fmt.Sprintf("%+v", a.status())}
	if err != nil {
		surfaces = append(surfaces, err.Error())
	}
	for _, s := range surfaces {
		for _, secret := range []string{plantedToken, plantedItem, "item 2815"} {
			if strings.Contains(s, secret) {
				t.Fatalf("%q leaks %q", s, secret)
			}
		}
	}
}

func TestReadKeychainItem(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		timeout     time.Duration
		cancelAfter time.Duration
		wantToken   string
		wantFailure streamsup.AccountTokenFailure
		wantReason  string
	}{
		{name: "success", body: "printf '%s\\n' " + plantedToken, wantToken: plantedToken},
		{name: "non-zero exit", body: "LEAK; exit 1", wantFailure: streamsup.AccountTokenReadFailure, wantReason: "keychain read failed"},
		{name: "empty", body: "exit 0", wantFailure: streamsup.AccountTokenEmptyOutput, wantReason: "keychain output empty"},
		{name: "invalid", body: "LEAK", wantFailure: streamsup.AccountTokenInvalidOutput, wantReason: "keychain output invalid"},
		{name: "hang cut by deadline", body: "LEAK; sleep 30 & wait", timeout: 200 * time.Millisecond, wantFailure: streamsup.AccountTokenTimeout, wantReason: "keychain read timed out"},
		{name: "hang cut by cancel", body: "LEAK; sleep 30 & wait", cancelAfter: 200 * time.Millisecond, wantFailure: streamsup.AccountTokenCancellation, wantReason: "keychain read cancelled"},
	}
	for _, p := range keychainPlatforms {
		for _, tc := range tests {
			t.Run(p.goos+"/"+tc.name, func(t *testing.T) {
				setKeychainGOOS(t, p.goos)
				writeFakeKeychainTool(t, p.tool, p.args, tc.body)
				a, log := keychainAccount(t)
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
				assertNoKeychainLeak(t, a, log, err)
			})
		}
	}
}

// A vector other than the platform's own is refused by the fake, so the
// other platform's tool on PATH never yields a token.
func TestReadKeychainItem_WrongVectorFails(t *testing.T) {
	setKeychainGOOS(t, "darwin")
	writeFakeKeychainTool(t, "security", keychainPlatforms[1].args, "printf '%s\\n' "+plantedToken)
	a, _ := keychainAccount(t)
	if tok, _, err := a.provider()(context.Background()); tok != "" || err == nil {
		t.Fatalf("got (%q, %v), want refusal", tok, err)
	}
}

func TestReadKeychainItem_MissingTool(t *testing.T) {
	for _, p := range keychainPlatforms {
		t.Run(p.goos, func(t *testing.T) {
			setKeychainGOOS(t, p.goos)
			t.Setenv("PATH", t.TempDir())
			a, log := keychainAccount(t)
			tok, failure, err := a.provider()(context.Background())
			if tok != "" || err == nil || failure != streamsup.AccountTokenReadFailure {
				t.Fatalf("got (%q, %q, %v)", tok, failure, err)
			}
			if got := a.status(); got.Reason != "keychain tool unavailable" {
				t.Fatalf("status = %+v", got)
			}
			assertNoKeychainLeak(t, a, log, err)
		})
	}
}

func TestNewClaudeAccount_KeychainSourceFromEachOrigin(t *testing.T) {
	setKeychainGOOS(t, "linux")
	writeFakeKeychainTool(t, "secret-tool", keychainPlatforms[1].args, "printf '%s\\n' "+plantedToken)
	dir := t.TempDir()
	writeAccountFile(t, dir, `{"source":"keychain:`+plantedItem+`"}`, 0o600)
	for _, sel := range []struct{ flag, env, dir string }{
		{flag: "keychain:" + plantedItem, dir: t.TempDir()},
		{env: "keychain:" + plantedItem, dir: t.TempDir()},
		{dir: dir},
	} {
		a, err := newClaudeAccount(sel.flag, sel.env, "", "", sel.dir, quietLogger())
		if err != nil {
			t.Fatal(err)
		}
		if tok, _, err := a.provider()(context.Background()); err != nil || tok != plantedToken {
			t.Fatalf("got (%q, %v)", tok, err)
		}
		if got := a.ClaudeAccount().Kind; got != protocol.ClaudeAccountKindOSKeychain {
			t.Fatalf("wire kind = %q", got)
		}
	}
}

func TestNewClaudeAccount_RefusesKeychainNameWithoutEchoingValue(t *testing.T) {
	setKeychainGOOS(t, "darwin")
	for _, value := range []string{"keychain:", "keychain:-w " + plantedItem, "keychain:" + plantedItem + "\x1b", "keychain:" + plantedItem + "\x7f"} {
		dir := t.TempDir()
		for _, sel := range []struct{ flag, env, file, origin string }{
			{flag: value, origin: "flag -" + claudeAccountFlagName},
			{env: value, origin: "env " + claudeAccountSourceEnv},
			{file: `{"source":` + fmt.Sprintf("%q", value) + `}`, origin: "file " + filepath.Join(dir, claudeAccountFileName)},
		} {
			if sel.file != "" {
				writeAccountFile(t, dir, sel.file, 0o600)
			}
			_, err := newClaudeAccount(sel.flag, sel.env, "", "", dir, quietLogger())
			if err == nil {
				t.Fatalf("source %q from %s accepted", value, sel.origin)
			}
			if !strings.Contains(err.Error(), sel.origin) || strings.Contains(err.Error(), "item 2815") {
				t.Fatalf("error %q: want origin %q and no value", err, sel.origin)
			}
		}
	}
}

func TestNewClaudeAccount_RefusesKeychainOnOtherPlatform(t *testing.T) {
	setKeychainGOOS(t, "freebsd")
	_, err := newClaudeAccount("keychain:"+plantedItem, "", "", "", t.TempDir(), quietLogger())
	if err == nil || !strings.Contains(err.Error(), "flag -"+claudeAccountFlagName) || strings.Contains(err.Error(), plantedItem) {
		t.Fatalf("err = %v", err)
	}
}
