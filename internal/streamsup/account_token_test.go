package streamsup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const tokenEnvName = "CLAUDE_CODE_OAUTH_TOKEN"
const privateToken = "credential-output-sentinel-2823"
const privateError = "credential-error-sentinel-2823"
const privateCategory = "credential-category-sentinel-2823"

func TestAccountTokenValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, output, wantToken, wantReason string
		category                            AccountTokenFailure
		err                                 error
	}{
		{name: "plain", output: "token", wantToken: "token"},
		{name: "LF", output: "token\n", wantToken: "token"},
		{name: "CRLF", output: "token\r\n", wantToken: "token"},
		{name: "empty", wantReason: "empty output"},
		{name: "only LF", output: "\n", wantReason: "empty output"},
		{name: "only CRLF", output: "\r\n", wantReason: "empty output"},
		{name: "two CRLF", output: "token\r\n\r\n", wantReason: "invalid output"},
		{name: "internal LF", output: "tok\nen", wantReason: "invalid output"},
		{name: "two LF", output: "token\n\n", wantReason: "invalid output"},
		{name: "CR", output: "token\r", wantReason: "invalid output"},
		{name: "space", output: " token", wantReason: "invalid output"},
		{name: "tab", output: "tok\ten", wantReason: "invalid output"},
		{name: "unicode", output: "tok\u2003en", wantReason: "invalid output"},
		{name: "NUL", output: "tok\x00en", wantReason: "invalid output"},
		{name: "error with token", output: privateToken, err: errors.New(privateError), wantReason: "read failure"},
		{name: "known category", output: privateToken, category: AccountTokenReadFailure, err: errors.New(privateError), wantReason: "read failure"},
		{name: "unknown category", output: privateToken, category: AccountTokenFailure(privateCategory), err: errors.New(privateError), wantReason: "rejected"},
		{name: "provider timeout category", output: privateToken, category: AccountTokenTimeout, wantReason: "timeout"},
		{name: "provider cancellation category", output: privateToken, category: AccountTokenCancellation, wantReason: "cancellation"},
		{name: "category without error", output: privateToken, category: AccountTokenInvalidOutput, wantReason: "invalid output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runner{cfg: Config{AccountTokenProvider: func(context.Context) (string, AccountTokenFailure, error) { return tc.output, tc.category, tc.err }}}
			base := []string{"KEEP=value", tokenEnvName + "=old", tokenEnvName + "=older"}
			before := slices.Clone(base)
			env, err := r.accountTokenEnv(context.Background(), base)
			if !reflect.DeepEqual(base, before) {
				t.Fatal("input environment mutated")
			}
			if tc.wantReason != "" {
				if err == nil || err.Error() != "streamsup: account token: "+tc.wantReason {
					t.Fatalf("unexpected safe error: %v", err)
				}
				if env != nil {
					t.Fatal("rejection returned environment")
				}
				if tc.err != nil && errors.Is(err, tc.err) {
					t.Fatal("private error exposed through unwrap")
				}
			} else if err != nil || !reflect.DeepEqual(env, []string{"KEEP=value", tokenEnvName + "=" + tc.wantToken}) {
				t.Fatalf("env=%v err=%v", env, err)
			}
		})
	}
}

type tokenWitness struct{ Args, Env []string }

func awaitTokenWitness(t *testing.T, b *safeBuffer, index int) tokenWitness {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		lines := strings.Split(b.String(), "\n")
		var witnesses []tokenWitness
		for _, line := range lines[:len(lines)-1] {
			var w tokenWitness
			if json.Unmarshal([]byte(line), &w) == nil {
				witnesses = append(witnesses, w)
			}
		}
		if len(witnesses) > index {
			return witnesses[index]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("missing actual child launch witness")
	return tokenWitness{}
}

func assertTokenWitness(t *testing.T, w tokenWitness, token, id, flag string) {
	t.Helper()
	var tokens []string
	for _, entry := range w.Env {
		if strings.HasPrefix(entry, tokenEnvName+"=") {
			tokens = append(tokens, entry)
		}
	}
	if !reflect.DeepEqual(tokens, []string{tokenEnvName + "=" + token}) {
		t.Fatalf("child token entries: %v", tokens)
	}
	if !slices.Contains(w.Env, "TOKEN_TEST_SESSION="+id) || !slices.Contains(w.Env, "TOKEN_TEST_KEEP=retained") {
		t.Fatal("session or unrelated environment lost")
	}
	for i, arg := range w.Args {
		if arg == flag && i+1 < len(w.Args) && w.Args[i+1] == id {
			return
		}
	}
	t.Fatalf("child did not use %s with current session: %v", flag, w.Args)
}

func tokenRunner(t *testing.T, provider AccountTokenProvider, extra ...string) (*Runner, *safeBuffer) {
	t.Helper()
	out := &safeBuffer{}
	cfg := helperRunCfg(t, "token_witness", out, &safeBuffer{}, append([]string{"TOKEN_TEST_KEEP=retained"}, extra...)...)
	cfg.SessionIDEnvVar = "TOKEN_TEST_SESSION"
	cfg.AccountTokenProvider = provider
	cfg.BackoffInitial = 20 * time.Millisecond
	cfg.BackoffMax = 20 * time.Millisecond
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r, out
}

func TestAccountTokenChildEnvironment(t *testing.T) {
	t.Setenv(tokenEnvName, "inherited")
	for _, tc := range []struct {
		name, configured, want string
		provider               AccountTokenProvider
	}{
		{name: "absent inherited", want: "inherited"},
		{name: "absent configured", configured: "configured", want: "configured"},
		{name: "successful inherited", want: "resolved", provider: func(context.Context) (string, AccountTokenFailure, error) { return "resolved", "", nil }},
		{name: "successful configured", configured: "configured", want: "resolved", provider: func(context.Context) (string, AccountTokenFailure, error) { return "resolved\r\n", "", nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var extra []string
			if tc.configured != "" {
				extra = append(extra, tokenEnvName+"="+tc.configured, tokenEnvName+"=duplicate")
			}
			// Absent providers preserve os/exec's last-entry-wins behavior.
			if tc.provider == nil && tc.configured != "" {
				extra = extra[:1]
			}
			r, out := tokenRunner(t, tc.provider, extra...)
			original := slices.Clone(r.cfg.Env)
			cancel, join := runInBackground(t, r)
			defer func() {
				cancel()
				if err := join(); !errors.Is(err, context.Canceled) {
					t.Errorf("shutdown: %v", err)
				}
			}()
			assertTokenWitness(t, awaitTokenWitness(t, out, 0), tc.want, testSessionID, "--session-id")
			if !reflect.DeepEqual(r.cfg.Env, original) || os.Getenv(tokenEnvName) != "inherited" {
				t.Fatal("config or parent environment mutated")
			}
			r.Restart(nil)
			assertTokenWitness(t, awaitTokenWitness(t, out, 1), tc.want, testSessionID, "--resume")
			if !reflect.DeepEqual(r.cfg.Env, original) || os.Getenv(tokenEnvName) != "inherited" {
				t.Fatal("restart mutated config or parent environment")
			}
		})
	}
}

func TestAccountTokenFreshReadEveryLaunch(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"crash", "restart", "fresh"} {
		t.Run(action, func(t *testing.T) {
			var reads atomic.Int32
			contexts := make(chan context.Context, 2)
			provider := func(ctx context.Context) (string, AccountTokenFailure, error) {
				contexts <- ctx
				return fmt.Sprintf("attempt-%d", reads.Add(1)), "", nil
			}
			r, out := tokenRunner(t, provider)
			cancel, join := runInBackground(t, r)
			defer func() { cancel(); join() }()
			assertTokenWitness(t, awaitTokenWitness(t, out, 0), "attempt-1", testSessionID, "--session-id")
			if (<-contexts).Err() == nil {
				t.Fatal("read context remains live after admission")
			}
			// A cancelled read context must not govern the admitted child's lifetime.
			writeTokenChild(t, r, "alive")
			waitForContains(t, out, "ALIVE", time.Second)
			id, flag := testSessionID, "--resume"
			switch action {
			case "crash":
				writeTokenChild(t, r, "crash")
			case "restart":
				r.Restart([]string{"--model", "new-model"})
			case "fresh":
				id = "fresh-session"
				flag = "--session-id"
				r.RestartFresh(id)
			}
			w := awaitTokenWitness(t, out, 1)
			assertTokenWitness(t, w, "attempt-2", id, flag)
			if action == "restart" && !slices.Contains(w.Args, "new-model") {
				t.Fatal("restart arguments lost")
			}
			if reads.Load() != 2 {
				t.Fatalf("reads=%d", reads.Load())
			}
		})
	}
}

func TestAccountTokenRejectionRecoveryAndDiagnostics(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, output, reason string
		category             AccountTokenFailure
		err                  error
	}{
		{name: "read", output: privateToken, err: errors.New(privateError), reason: "read failure"},
		{name: "empty", output: "", reason: "empty output"},
		{name: "invalid", output: privateToken + "\u2003", reason: "invalid output"},
		{name: "unknown", output: privateToken, category: AccountTokenFailure(privateCategory), err: errors.New(privateError), reason: "rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &logRecorder{}
			var reads, exits atomic.Int32
			r, out := tokenRunner(t, func(context.Context) (string, AccountTokenFailure, error) {
				if reads.Add(1) == 1 {
					return tc.output, tc.category, tc.err
				}
				return "recovered", "", nil
			}, tokenEnvName+"=fallback")
			r.cfg.Logger = slog.New(rec)
			r.log = r.cfg.Logger
			r.cfg.OnChildExit = func() { exits.Add(1) }
			r.cfg.BackoffInitial = 80 * time.Millisecond
			r.cfg.BackoffMax = 80 * time.Millisecond
			cancel, join := runInBackground(t, r)
			defer func() { cancel(); join() }()
			w := awaitTokenWitness(t, out, 0)
			assertTokenWitness(t, w, "recovered", testSessionID, "--session-id")
			if reads.Load() != 2 || exits.Load() != 1 || r.State().RestartCount != 1 {
				t.Fatal("rejection did not use setup-failure retry/callback lifecycle")
			}
			records := rec.withMessage("claude exited")
			if len(records) != 1 || records[0].attrs["err"] != "streamsup: account token: "+tc.reason {
				t.Fatalf("missing safe rejection diagnostic: %v", records)
			}
			for _, record := range rec.all() {
				texts := []string{record.msg}
				for key, value := range record.attrs {
					texts = append(texts, key, value)
				}
				for _, text := range texts {
					for _, secret := range []string{privateToken, privateError, privateCategory, "fallback"} {
						if strings.Contains(text, secret) {
							t.Fatal("private credential data in diagnostics")
						}
					}
				}
			}
			if strings.Count(out.String(), "\"Args\"") != 1 {
				t.Fatal("rejected attempt launched a child")
			}
		})
	}
}

func TestAccountTokenBlockedReadCancellation(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"restart", "fresh", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			entered := make(chan context.Context, 1)
			var reads atomic.Int32
			r, out := tokenRunner(t, func(ctx context.Context) (string, AccountTokenFailure, error) {
				if reads.Add(1) == 1 {
					entered <- ctx
					<-ctx.Done()
					return privateToken, "", nil
				}
				return "next-attempt", "", nil
			})
			cancel, join := runInBackground(t, r)
			joined := false
			defer func() {
				cancel()
				if !joined {
					join()
				}
			}()
			var readCtx context.Context
			select {
			case readCtx = <-entered:
			case <-time.After(time.Second):
				t.Fatal("provider never entered")
			}
			deadline, ok := readCtx.Deadline()
			if !ok || time.Until(deadline) > 10*time.Second || time.Until(deadline) < 9*time.Second {
				t.Fatal("missing ten-second read deadline")
			}
			start := time.Now()
			id := testSessionID
			switch action {
			case "restart":
				r.Restart([]string{"--model", "after-cancel"})
			case "fresh":
				id = "rotated-during-read"
				r.RestartFresh(id)
			case "shutdown":
				cancel()
				err := join()
				joined = true
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("shutdown: %v", err)
				}
				if out.String() != "" || reads.Load() != 1 {
					t.Fatal("shutdown or late success launched child")
				}
			}
			if action != "shutdown" {
				w := awaitTokenWitness(t, out, 0)
				assertTokenWitness(t, w, "next-attempt", id, "--session-id")
				if action == "restart" && !slices.Contains(w.Args, "after-cancel") {
					t.Fatal("restart arguments lost")
				}
				if reads.Load() != 2 || strings.Count(out.String(), "\"Args\"") != 1 {
					t.Fatal("cancelled read launched a child")
				}
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("cancellation waited for read deadline")
			}
		})
	}
}

func TestAccountTokenReadDeadline(t *testing.T) {
	t.Parallel()
	rec := &logRecorder{}
	entered := make(chan struct{})
	var reads atomic.Int32
	r, out := tokenRunner(t, func(ctx context.Context) (string, AccountTokenFailure, error) {
		if reads.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			return privateToken, "", nil
		}
		return "after-timeout", "", nil
	})
	r.log = slog.New(rec)
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("provider never entered")
	}
	// Wait through the real production deadline; no mutable clock/timeout seam.
	time.Sleep(10200 * time.Millisecond)
	assertTokenWitness(t, awaitTokenWitness(t, out, 0), "after-timeout", testSessionID, "--session-id")
	rows := rec.withMessage("claude exited")
	if len(rows) != 1 || rows[0].attrs["err"] != "streamsup: account token: timeout" {
		t.Fatalf("deadline diagnostic: %v", rows)
	}
	if reads.Load() != 2 || strings.Count(out.String(), "\"Args\"") != 1 {
		t.Fatal("expired read launched a child")
	}
}

func TestAccountTokenCancelledResult(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &Runner{cfg: Config{AccountTokenProvider: func(context.Context) (string, AccountTokenFailure, error) { return privateToken, "", nil }}}
	env, err := r.accountTokenEnv(ctx, nil)
	if env != nil || err == nil || err.Error() != "streamsup: account token: cancellation" {
		t.Fatalf("late success admitted: %v", err)
	}
}

func TestAccountTokenRejectionAfterAdmittedChild(t *testing.T) {
	t.Parallel()
	var reads, exits atomic.Int32
	rec := &logRecorder{}
	r, out := tokenRunner(t, func(context.Context) (string, AccountTokenFailure, error) {
		switch reads.Add(1) {
		case 1:
			return "first-token", "", nil
		case 2:
			return privateToken, AccountTokenReadFailure, errors.New(privateError)
		default:
			return "recovered-token", "", nil
		}
	}, tokenEnvName+"=fallback")
	r.log = slog.New(rec)
	r.cfg.OnChildExit = func() { exits.Add(1) }
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()
	assertTokenWitness(t, awaitTokenWitness(t, out, 0), "first-token", testSessionID, "--session-id")
	writeTokenChild(t, r, "crash")
	assertTokenWitness(t, awaitTokenWitness(t, out, 1), "recovered-token", testSessionID, "--resume")
	if reads.Load() != 3 || exits.Load() != 2 || strings.Count(out.String(), "\"Args\"") != 2 {
		t.Fatal("rejection launched with a prior or fallback token")
	}
	rows := rec.withMessage("claude exited")
	if len(rows) != 2 || rows[1].attrs["err"] != "streamsup: account token: read failure" {
		t.Fatalf("missing rejection after child exit: %v", rows)
	}
}

func writeTokenChild(t *testing.T, r *Runner, command string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if stdin := r.Stdin(); stdin != nil {
			if _, err := stdin.Write([]byte(command + "\n")); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("child stdin was not published")
}
