package claudetape

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
)

func TestLifecycleDiagnosticErrorClass(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{{nil, "none"}, {fmt.Errorf("private payload: %w", syscall.EPIPE), "broken-pipe"}, {os.ErrDeadlineExceeded, "deadline"}, {errors.New("secret payload /private/path"), "rejected"}} {
		if got := lifecycleErrorClass(tc.err); got != tc.want {
			t.Fatalf("class=%q want=%q", got, tc.want)
		}
	}
}
