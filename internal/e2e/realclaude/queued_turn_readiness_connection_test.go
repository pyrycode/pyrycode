//go:build e2e_realclaude

package realclaude

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
)

func TestReadinessConnectionFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"timeout", fmt.Errorf("private token: %w", fakephone.ErrReceiveTimeout), "deadline"},
		{"cancelled", context.Canceled, "cancelled"},
		{"closed", fakephone.ErrClosed, "closed"},
		{"network closed", net.ErrClosed, "closed"},
		{"oversized", websocket.CloseError{Code: websocket.StatusMessageTooBig, Reason: "private content"}, "websocket-1009"},
		{"unknown", errors.New("private path /private/token"), "io-failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := readinessConnectionFailure(tc.err); got != tc.want {
				t.Fatalf("category=%q, want %q", got, tc.want)
			}
		})
	}
}

// readinessConnectionFailure retains only public categories, never an error's text.
func readinessConnectionFailure(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, fakephone.ErrReceiveTimeout):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, fakephone.ErrClosed), errors.Is(err, net.ErrClosed):
		return "closed"
	}
	if code := websocket.CloseStatus(err); code != -1 {
		return fmt.Sprintf("websocket-%d", code)
	}
	return "io-failure"
}

func TestReadinessReconnectCaseOwnership(t *testing.T) {
	if os.Getenv("PYRY_READINESS_CASE_FAILURE_HELPER") == "1" {
		p := &readinessReplay{}
		p.reconnect = func(caseT *testing.T) {
			if caseT != p.t {
				t.Fatal("wrong callback owner")
			}
			caseT.Fatal("fixture connection failure")
		}
		passed := t.Run("reconnect", func(caseT *testing.T) {
			p.t = caseT
			p.operation("reconnect")
		})
		t.Logf("case outcome retained: passed=%t", passed)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReadinessReconnectCaseOwnership$", "-test.v")
	cmd.Env = append(os.Environ(), "PYRY_READINESS_CASE_FAILURE_HELPER=1")
	out, err := cmd.CombinedOutput()
	if err == nil || ctx.Err() != nil || !strings.Contains(string(out), "case outcome retained: passed=false") || strings.Contains(string(out), "subtest may have called FailNow on a parent test") {
		t.Fatalf("failure was not accounted by its case: err=%v\n%s", err, out)
	}
}
