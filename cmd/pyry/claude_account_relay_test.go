package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/streamsup"
)

func TestNewClaudeAccount_Label(t *testing.T) {
	const tokenPath = "/nonexistent/token-2839"
	long := strings.Repeat("ä", maxAccountLabelBytes/2)
	tests := []struct {
		name      string
		flag, env string
		file      string
		wantLabel string
		wantErr   bool
		secret    string // must not appear in the error
	}{
		{name: "file source with label", file: `{"source":"` + tokenPath + `","label":"Work account"}`, wantLabel: "Work account"},
		{name: "file source without label", file: `{"source":"` + tokenPath + `"}`},
		{name: "exactly the limit", file: `{"source":"` + tokenPath + `","label":"` + long + `"}`, wantLabel: long},
		{name: "flag source ignores label", flag: tokenPath, file: `{"label":"Work account"}`},
		{name: "env source ignores label", env: tokenPath, file: `{"label":"Work account"}`},
		{name: "flag source ignores bad label", flag: tokenPath, file: `{"label":42}`},
		{name: "no source ignores label", file: `{"label":"Work account"}`},
		{name: "over the limit", file: `{"source":"` + tokenPath + `","label":"` + long + `x"}`, wantErr: true, secret: long},
		{name: "invalid UTF-8", file: `{"source":"` + tokenPath + `","label":"bad-label-2839-` + "\xff" + `"}`, wantErr: true, secret: "bad-label-2839"},
		{name: "C0 control", file: `{"source":"` + tokenPath + `","label":"bad-label-2839\u001b[2J"}`, wantErr: true, secret: "bad-label-2839"},
		{name: "C1 control", file: `{"source":"` + tokenPath + `","label":"bad-label-2839\u0085"}`, wantErr: true, secret: "bad-label-2839"},
		{name: "non-string", file: `{"source":"` + tokenPath + `","label":["bad-label-2839"]}`, wantErr: true, secret: "bad-label-2839"},
		{name: "null", file: `{"source":"` + tokenPath + `","label":null}`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeAccountFile(t, dir, tc.file, 0o600)
			a, err := newClaudeAccount(tc.flag, tc.env, "", "", dir, quietLogger())
			if tc.wantErr {
				origin := "file " + filepath.Join(dir, claudeAccountFileName)
				if err == nil || !strings.Contains(err.Error(), origin) {
					t.Fatalf("err = %v, want one naming %q", err, origin)
				}
				if tc.secret != "" && strings.Contains(err.Error(), tc.secret) {
					t.Fatalf("error %q echoes the label", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := a.status().Label; got != tc.wantLabel {
				t.Fatalf("label = %q, want %q", got, tc.wantLabel)
			}
		})
	}
}

// gatedRead is one scripted read: it signals entered once it has its read
// number, then waits for release before reporting reason ("" is a success).
type gatedRead struct {
	entered, release chan struct{}
	reason           string
}

func TestClaudeAccount_LaterReadWins(t *testing.T) {
	tests := []struct {
		name                  string
		earlier, later        string
		wantState, wantReason string
	}{
		{name: "fail after success", earlier: "token file missing", wantState: claudeAccountReady},
		{name: "success after fail", later: "token file empty", wantState: claudeAccountFailed, wantReason: "token file empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			steps := make(chan gatedRead, 2)
			a := &claudeAccount{kind: "file", logger: quietLogger(), state: claudeAccountNotConfigured}
			a.reader = func(context.Context) (string, streamsup.AccountTokenFailure, error) {
				step := <-steps
				close(step.entered)
				<-step.release
				if step.reason != "" {
					return accountReadFailure(streamsup.AccountTokenReadFailure, step.reason)
				}
				return plantedToken, "", nil
			}
			first := gatedRead{entered: make(chan struct{}), release: make(chan struct{}), reason: tc.earlier}
			steps <- first
			type result struct {
				tok string
				err error
			}
			done := make(chan result, 1)
			go func() {
				tok, _, err := a.read(context.Background())
				done <- result{tok, err}
			}()
			<-first.entered

			second := gatedRead{entered: make(chan struct{}), release: make(chan struct{}), reason: tc.later}
			close(second.release)
			steps <- second
			_, _, _ = a.read(context.Background())

			close(first.release)
			r := <-done
			if (r.err != nil) != (tc.earlier != "") || (r.err == nil && r.tok != plantedToken) {
				t.Fatalf("earlier read's own result = (%q, %v)", r.tok, r.err)
			}
			if got := a.status(); got.State != tc.wantState || got.Reason != tc.wantReason {
				t.Fatalf("status = %+v, want the later read's %s/%q", got, tc.wantState, tc.wantReason)
			}
		})
	}
}

func TestClaudeAccount_WirePayload(t *testing.T) {
	tests := []struct {
		in   claudeAccountStatus
		want protocol.ClaudeAccountPayload
	}{
		{in: claudeAccountStatus{State: claudeAccountNotConfigured}, want: protocol.ClaudeAccountPayload{Kind: protocol.ClaudeAccountKindMachineLogin, State: protocol.ClaudeAccountStateNotConfigured}},
		{in: claudeAccountStatus{Kind: "file", Label: "work", State: claudeAccountReady}, want: protocol.ClaudeAccountPayload{Kind: protocol.ClaudeAccountKindFile, Label: "work", State: protocol.ClaudeAccountStateReady}},
		{in: claudeAccountStatus{Kind: "1password", Label: "vault", State: claudeAccountFailed, Reason: "1Password read failed"}, want: protocol.ClaudeAccountPayload{Kind: protocol.ClaudeAccountKindOnePassword, Label: "vault", State: protocol.ClaudeAccountStateFailed, Reason: "1Password read failed"}},
	}
	for _, tc := range tests {
		if got := claudeAccountPayload(tc.in); got != tc.want {
			t.Errorf("claudeAccountPayload(%+v) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

// requestClaudeAccountFrame sends request_claude_account through the relay
// handler and returns the serialized reply frame and its payload.
func requestClaudeAccountFrame(t *testing.T, a *claudeAccount) ([]byte, protocol.ClaudeAccountPayload) {
	t.Helper()
	w := relayWiring{claudeAccount: a}
	out := make(chan protocol.RoutingEnvelope, 1)
	conn := dispatch.NewTestConn("requester", out, nil)
	req := protocol.Envelope{ID: 2839, Type: protocol.TypeRequestClaudeAccount, Payload: json.RawMessage(`{}`)}
	if err := handlers.RequestClaudeAccount(w.claudeAccount, quietLogger())(context.Background(), conn, req); err != nil {
		t.Fatal(err)
	}
	routed := <-out
	var env protocol.Envelope
	if err := json.Unmarshal(routed.Frame, &env); err != nil {
		t.Fatal(err)
	}
	if env.Type != protocol.TypeClaudeAccount || env.InReplyTo == nil || *env.InReplyTo != req.ID {
		t.Fatalf("reply %s is not a correlated claude_account", env.Type)
	}
	var p protocol.ClaudeAccountPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatal(err)
	}
	return routed.Frame, p
}

func assertFrameHidesAll(t *testing.T, frame []byte, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(string(frame), s) {
			t.Fatalf("claude_account frame %s leaks %q", frame, s)
		}
	}
}

func TestClaudeAccountFrame_NotConfigured(t *testing.T) {
	a, err := newClaudeAccount("", "", "", "", t.TempDir(), quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	a.prime(context.Background())
	_, got := requestClaudeAccountFrame(t, a)
	if want := (protocol.ClaudeAccountPayload{Kind: protocol.ClaudeAccountKindMachineLogin, State: protocol.ClaudeAccountStateNotConfigured}); got != want {
		t.Fatalf("payload = %+v, want %+v", got, want)
	}
}

func TestClaudeAccountFrame_FileSourceNoLeak(t *testing.T) {
	instanceDir := filepath.Join(t.TempDir(), "instance-dir-2839-qv")
	tokenPath := filepath.Join(t.TempDir(), "token-dir-2839-zk", "planted-token-file")
	writeOwnerToken(t, tokenPath, plantedToken)
	if err := os.MkdirAll(instanceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeAccountFile(t, instanceDir, `{"source":"`+tokenPath+`","label":"Work"}`, 0o600)
	jsonPath := filepath.Join(instanceDir, claudeAccountFileName)
	a, err := newClaudeAccount("", "", "", "", instanceDir, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	secrets := []string{plantedToken, tokenPath, "token-dir-2839-zk", jsonPath, "instance-dir-2839-qv"}

	a.prime(context.Background())
	frame, got := requestClaudeAccountFrame(t, a)
	if want := (protocol.ClaudeAccountPayload{Kind: protocol.ClaudeAccountKindFile, Label: "Work", State: protocol.ClaudeAccountStateReady}); got != want {
		t.Fatalf("ready payload = %+v, want %+v", got, want)
	}
	assertFrameHidesAll(t, frame, secrets...)

	if err := os.Chmod(tokenPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.provider()(context.Background()); err == nil {
		t.Fatal("loose token file read succeeded")
	}
	frame, got = requestClaudeAccountFrame(t, a)
	if want := (protocol.ClaudeAccountPayload{Kind: protocol.ClaudeAccountKindFile, Label: "Work", State: protocol.ClaudeAccountStateFailed, Reason: "token file allows group or other access"}); got != want {
		t.Fatalf("failed payload = %+v, want %+v", got, want)
	}
	assertFrameHidesAll(t, frame, secrets...)

	if err := os.Chmod(tokenPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.provider()(context.Background()); err != nil {
		t.Fatal(err)
	}
	frame, got = requestClaudeAccountFrame(t, a)
	if got.State != protocol.ClaudeAccountStateReady || got.Reason != "" {
		t.Fatalf("recovered payload = %+v", got)
	}
	assertFrameHidesAll(t, frame, secrets...)
}

func TestClaudeAccountFrame_OnePasswordNoLeak(t *testing.T) {
	const ref = "op://vault-2839-mx7q/item-2839-pk4w/credential"
	tokenPath := filepath.Join(t.TempDir(), "op-token-2839")
	cli := filepath.Join(t.TempDir(), "op")
	script := "#!/bin/sh\n[ \"$2\" = '" + ref + "' ] || exit 7\ncat '" + tokenPath + "' 2>/dev/null || { echo \"$2 " + plantedToken + "\" >&2; exit 1; }\n"
	if err := os.WriteFile(cli, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	writeOwnerToken(t, tokenPath, plantedToken)
	instanceDir := t.TempDir()
	writeAccountFile(t, instanceDir, `{"source":"`+ref+`","op_cli":"`+cli+`","label":"Personal 1P"}`, 0o600)
	a, err := newClaudeAccount("", "", "", "", instanceDir, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	secrets := []string{plantedToken, ref, "vault-2839-mx7q", "item-2839-pk4w", filepath.Join(instanceDir, claudeAccountFileName)}

	a.prime(context.Background())
	frame, got := requestClaudeAccountFrame(t, a)
	if want := (protocol.ClaudeAccountPayload{Kind: protocol.ClaudeAccountKindOnePassword, Label: "Personal 1P", State: protocol.ClaudeAccountStateReady}); got != want {
		t.Fatalf("ready payload = %+v, want %+v", got, want)
	}
	assertFrameHidesAll(t, frame, secrets...)

	if err := os.Remove(tokenPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.provider()(context.Background()); err == nil {
		t.Fatal("locked item read succeeded")
	}
	frame, got = requestClaudeAccountFrame(t, a)
	if want := (protocol.ClaudeAccountPayload{Kind: protocol.ClaudeAccountKindOnePassword, Label: "Personal 1P", State: protocol.ClaudeAccountStateFailed, Reason: "1Password read failed"}); got != want {
		t.Fatalf("failed payload = %+v, want %+v", got, want)
	}
	assertFrameHidesAll(t, frame, secrets...)
}
