package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
)

const plantedToken = "sk-ant-oat01-planted-2824"

func writeAccountFile(t *testing.T, dir, body string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(dir, claudeAccountFileName)
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestResolveClaudeAccountSource(t *testing.T) {
	tests := []struct {
		name       string
		flag, env  string
		file       string // "" means no file
		fileMode   os.FileMode
		wantSource string
		wantOrigin string // substring; "" with wantSource "" means not configured
		wantErr    string // substring naming the origin
	}{
		{name: "nothing configured"},
		{name: "flag wins over env and file", flag: "/a", env: "/b", file: `{"source":"/c"}`, wantSource: "/a", wantOrigin: "flag"},
		{name: "env wins over file", env: "/b", file: `{"source":"/c"}`, wantSource: "/b", wantOrigin: "env"},
		{name: "file used last", file: `{"source":"/c"}`, wantSource: "/c", wantOrigin: "file"},
		{name: "file not read when flag set", flag: "/a", file: `not json`, wantSource: "/a", wantOrigin: "flag"},
		{name: "file not read when env set", env: "/b", file: `not json`, wantSource: "/b", wantOrigin: "env"},
		{name: "unknown keys ignored", file: `{"op_cli":"/usr/bin/op","source":"/c","extra":1}`, wantSource: "/c", wantOrigin: "file"},
		{name: "file without source", file: `{"op_cli":"/usr/bin/op"}`},
		{name: "empty source", file: `{"source":""}`},
		{name: "invalid json", file: `{"source":`, wantErr: "file"},
		{name: "non-string source", file: `{"source":42}`, wantErr: "file"},
		{name: "null source", file: `{"source":null}`, wantErr: "file"},
		{name: "group-writable file", file: `{"source":"/c"}`, fileMode: 0o620, wantErr: "file"},
		{name: "other-writable file", file: `{"source":"/c"}`, fileMode: 0o602, wantErr: "file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.file != "" {
				mode := tc.fileMode
				if mode == 0 {
					mode = 0o600
				}
				writeAccountFile(t, dir, tc.file, mode)
			}
			source, origin, err := resolveClaudeAccountSource(tc.flag, tc.env, dir)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one naming %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if source != tc.wantSource || !strings.Contains(origin, tc.wantOrigin) {
				t.Fatalf("got (%q, %q), want (%q, origin %q)", source, origin, tc.wantSource, tc.wantOrigin)
			}
		})
	}
}

func TestResolveClaudeAccountSource_UnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads mode-000 files")
	}
	dir := t.TempDir()
	writeAccountFile(t, dir, `{"source":"/c"}`, 0o000)
	if _, _, err := resolveClaudeAccountSource("", "", dir); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("err = %v, want unreadable refusal", err)
	}
}

func TestResolveClaudeAccountSource_FIFORefusedWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, claudeAccountFileName), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := resolveClaudeAccountSource("", "", dir)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("err = %v, want regular-file refusal", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resolving a FIFO selection file blocked")
	}
}

func TestNewClaudeAccount_RefusesWithoutEchoingValue(t *testing.T) {
	for _, value := range []string{plantedToken, "relative/token", "file:///" + plantedToken, "op://", "op://vault/item/" + plantedToken + "\n"} {
		dir := t.TempDir()
		for _, sel := range []struct{ flag, env, file, origin string }{
			{flag: value, origin: "flag -pyry-claude-account-source"},
			{env: value, origin: "env PYRY_CLAUDE_ACCOUNT_SOURCE"},
			{file: `{"source":"` + value + `"}`, origin: "file " + filepath.Join(dir, claudeAccountFileName)},
		} {
			if sel.file != "" {
				writeAccountFile(t, dir, sel.file, 0o600)
			}
			_, err := newClaudeAccount(sel.flag, sel.env, "", "", dir, quietLogger())
			if err == nil {
				t.Fatalf("source from %s accepted", sel.origin)
			}
			if !strings.Contains(err.Error(), sel.origin) || strings.Contains(err.Error(), value) {
				t.Fatalf("error %q: want origin %q and no value", err, sel.origin)
			}
		}
	}
}

func TestNewClaudeAccount_NotConfigured(t *testing.T) {
	a, err := newClaudeAccount("", "", "", "", t.TempDir(), quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if a.provider() != nil {
		t.Fatal("unconfigured accessor installed a provider")
	}
	a.prime(context.Background())
	if got := a.status(); got != (claudeAccountStatus{State: claudeAccountNotConfigured}) {
		t.Fatalf("status = %+v", got)
	}
}

func TestReadTokenFile(t *testing.T) {
	big := strings.Repeat("a", maxAccountTokenBytes+1)
	tests := []struct {
		name        string
		content     string
		mode        os.FileMode
		setup       func(t *testing.T, path string) // replaces the regular file
		wantToken   string
		wantFailure streamsup.AccountTokenFailure
	}{
		{name: "0600 with LF", content: plantedToken + "\n", mode: 0o600, wantToken: plantedToken},
		{name: "0400 with CRLF", content: plantedToken + "\r\n", mode: 0o400, wantToken: plantedToken},
		{name: "no newline", content: plantedToken, mode: 0o600, wantToken: plantedToken},
		{name: "exactly at cap", content: big[:maxAccountTokenBytes], mode: 0o600, wantToken: big[:maxAccountTokenBytes]},
		{name: "group readable", content: plantedToken, mode: 0o640, wantFailure: streamsup.AccountTokenReadFailure},
		{name: "other readable", content: plantedToken, mode: 0o604, wantFailure: streamsup.AccountTokenReadFailure},
		{name: "group exec bit", content: plantedToken, mode: 0o610, wantFailure: streamsup.AccountTokenReadFailure},
		{name: "empty", content: "", mode: 0o600, wantFailure: streamsup.AccountTokenEmptyOutput},
		{name: "newline only", content: "\n", mode: 0o600, wantFailure: streamsup.AccountTokenEmptyOutput},
		{name: "oversized", content: big, mode: 0o600, wantFailure: streamsup.AccountTokenInvalidOutput},
		{name: "two lines", content: plantedToken + "\n" + plantedToken + "\n", mode: 0o600, wantFailure: streamsup.AccountTokenInvalidOutput},
		{name: "two trailing newlines", content: plantedToken + "\n\n", mode: 0o600, wantFailure: streamsup.AccountTokenInvalidOutput},
		{name: "env assignment with space", content: "export CLAUDE_CODE_OAUTH_TOKEN=" + plantedToken, mode: 0o600, wantFailure: streamsup.AccountTokenInvalidOutput},
		{name: "embedded NUL", content: plantedToken + "\x00x", mode: 0o600, wantFailure: streamsup.AccountTokenInvalidOutput},
		{name: "non-ASCII", content: plantedToken + "é", mode: 0o600, wantFailure: streamsup.AccountTokenInvalidOutput},
		{name: "missing", setup: func(t *testing.T, path string) {}, wantFailure: streamsup.AccountTokenReadFailure},
		{name: "directory", setup: func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}, wantFailure: streamsup.AccountTokenReadFailure},
		{name: "fifo", setup: func(t *testing.T, path string) {
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
		}, wantFailure: streamsup.AccountTokenReadFailure},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			if tc.setup != nil {
				tc.setup(t, path)
			} else {
				if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, tc.mode); err != nil {
					t.Fatal(err)
				}
			}
			type result struct {
				token   string
				failure streamsup.AccountTokenFailure
				err     error
			}
			done := make(chan result, 1)
			go func() {
				tok, f, err := readTokenFile(context.Background(), path)
				done <- result{tok, f, err}
			}()
			var got result
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("read blocked")
			}
			if tc.wantFailure == "" {
				if got.err != nil || got.failure != "" || got.token != tc.wantToken {
					t.Fatalf("got (%q, %q, %v), want token", got.token, got.failure, got.err)
				}
				return
			}
			if got.err == nil || got.failure != tc.wantFailure || got.token != "" {
				t.Fatalf("got (%q, %q, %v), want refusal %q", got.token, got.failure, got.err, tc.wantFailure)
			}
			if strings.Contains(got.err.Error(), path) || strings.Contains(got.err.Error(), plantedToken) {
				t.Fatalf("error %q leaks the path or token", got.err)
			}
		})
	}
}

func TestReadTokenFile_HonoursCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeOwnerToken(t, path, plantedToken)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if tok, f, err := readTokenFile(ctx, path); tok != "" || err == nil || f != streamsup.AccountTokenCancellation {
		t.Fatalf("got (%q, %q, %v), want cancellation", tok, f, err)
	}
}

// configuredAccount builds a file-source accessor over path with a captured log.
func configuredAccount(t *testing.T, path string) (*claudeAccount, *safeLog) {
	t.Helper()
	log := &safeLog{}
	a, err := newClaudeAccount(path, "", "", "", t.TempDir(), testLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	return a, log
}

func TestClaudeAccount_RecoveryAndRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeOwnerToken(t, path, plantedToken)
	a, log := configuredAccount(t, path)
	provider := a.provider()
	if provider == nil {
		t.Fatal("configured accessor installed no provider")
	}
	a.prime(context.Background())
	if got := a.status(); got != (claudeAccountStatus{Kind: "file", State: claudeAccountReady}) {
		t.Fatalf("after prime: %+v", got)
	}

	// Loosened permissions fail the read with no fallback to the earlier token.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	tok, failure, err := provider(context.Background())
	if tok != "" || err == nil || failure != streamsup.AccountTokenReadFailure {
		t.Fatalf("loose file: got (%q, %q, %v)", tok, failure, err)
	}
	if got := a.status(); got.State != claudeAccountFailed || got.Reason != "token file allows group or other access" {
		t.Fatalf("after failure: %+v", got)
	}

	// Restored permissions clear the failure on the next read.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if tok, _, err := provider(context.Background()); err != nil || tok != plantedToken {
		t.Fatalf("recovered read: (%q, %v)", tok, err)
	}
	if got := a.status(); got.State != claudeAccountReady || got.Reason != "" {
		t.Fatalf("after recovery: %+v", got)
	}

	// A rotated file takes effect at the next read.
	const rotated = "sk-ant-oat01-rotated-2824"
	next := path + ".new"
	writeOwnerToken(t, next, rotated)
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
	if tok, _, err := provider(context.Background()); err != nil || tok != rotated {
		t.Fatalf("rotated read: (%q, %v)", tok, err)
	}

	// Removal fails without falling back to the rotated token.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if tok, _, err := provider(context.Background()); tok != "" || err == nil {
		t.Fatalf("removed file: (%q, %v)", tok, err)
	}

	logged := log.String()
	if !strings.Contains(logged, "token file allows group or other access") || !strings.Contains(logged, "recovered") {
		t.Fatalf("failure and recovery not logged:\n%s", logged)
	}
	for _, secret := range []string{plantedToken, rotated, path} {
		if strings.Contains(logged, secret) {
			t.Fatalf("log leaks %q", secret)
		}
		if strings.Contains(fmt.Sprintf("%+v", a.status()), secret) {
			t.Fatalf("status leaks %q", secret)
		}
	}
}

func TestClaudeAccount_StartupReadFailureIsRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-token")
	a, log := configuredAccount(t, path)
	a.prime(context.Background())
	if got := a.status(); got != (claudeAccountStatus{Kind: "file", State: claudeAccountFailed, Reason: "token file missing"}) {
		t.Fatalf("status = %+v", got)
	}
	if !strings.Contains(log.String(), "claude launch refused") || strings.Contains(log.String(), path) {
		t.Fatalf("startup failure log wrong:\n%s", log.String())
	}
}

func TestClaudeAccount_ReasonForProviderCategories(t *testing.T) {
	for _, tc := range []struct {
		failure streamsup.AccountTokenFailure
		err     error
		want    string
	}{
		{streamsup.AccountTokenCancellation, context.Canceled, "token read cancelled"},
		{streamsup.AccountTokenTimeout, context.DeadlineExceeded, "token read timed out"},
		{"", errors.New("private " + plantedToken), "token read failed"},
	} {
		var buf bytes.Buffer
		a := &claudeAccount{kind: "file", logger: testLogger(&buf), state: claudeAccountNotConfigured,
			reader: func(context.Context) (string, streamsup.AccountTokenFailure, error) {
				return plantedToken, tc.failure, tc.err
			}}
		tok, _, _ := a.read(context.Background())
		if tok != "" || a.status().Reason != tc.want || strings.Contains(buf.String(), plantedToken) {
			t.Fatalf("%q: token %q, status %+v, log %q", tc.want, tok, a.status(), buf.String())
		}
	}
}
