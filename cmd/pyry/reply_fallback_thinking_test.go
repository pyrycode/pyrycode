package main

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
)

func TestReplyFallbackThinkingIsolation(t *testing.T) {
	for _, tc := range []struct{ name, budget string }{
		{"default thinking", ""}, {"ambient thinking", "31999"}, {"already disabled", "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MAX_THINKING_TOKENS", tc.budget)
			calls, reads := 0, 0
			var child *exec.Cmd
			f := replyFallback{
				account: func(context.Context) (string, streamsup.AccountTokenFailure, error) {
					reads++
					return "selected", "", nil
				},
				command: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
					calls++
					child = replyFallbackTestCommand(ctx, []string{"PYRY_REPLY_HELPER=1",
						"PYRY_REPLY_USER=user", "PYRY_REPLY_ASSISTANT=assistant",
						"PYRY_REPLY_THINKING_SCENARIO=1", "PYRY_REPLY_OUTPUT=Run the tests."})
					return child
				},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			text, err := f.run(ctx, "user", "assistant")
			if err != nil || text != "Run the tests." || ctx.Err() != nil || reads != 1 || calls != 1 {
				t.Fatal("one selected-account attempt spent its reply deadline in thinking")
			}
			if child.ProcessState == nil || !child.ProcessState.Success() {
				t.Fatal("valid reply lacks successful process completion")
			}
		})
	}
}

func TestReplyFallbackThinkingEnv(t *testing.T) {
	t.Parallel()
	for _, budget := range [][]string{nil, {"MAX_THINKING_TOKENS=31999"},
		{"MAX_THINKING_TOKENS=0", "MAX_THINKING_TOKENS=invalid"}} {
		env := append(slices.Clone(budget), "CLAUDE_CODE_OAUTH_TOKEN=private-test-token", "UNRELATED=keep")
		got := replyFallbackEnv(env)
		entries := 0
		for _, entry := range got {
			if strings.HasPrefix(entry, "MAX_THINKING_TOKENS=") {
				entries++
				if entry != "MAX_THINKING_TOKENS=0" {
					t.Fatal("ambient budget can re-enable isolated thinking")
				}
			}
		}
		if entries != 1 || !slices.Contains(got, "CLAUDE_CODE_OAUTH_TOKEN=private-test-token") || !slices.Contains(got, "UNRELATED=keep") {
			t.Fatal("thinking override must be unique and preserve unrelated/account variables")
		}
	}
}
