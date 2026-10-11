package memorycapture

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func testOperationError(phase, worker string, err error) error {
	if err == nil {
		return nil
	}
	var errno unix.Errno
	_ = errors.As(err, &errno)
	return fmt.Errorf("phase=%s worker=%s errno=%d: %w", phase, worker, errno, err)
}

func testPlatform(t *testing.T, ctx context.Context) {
	t.Helper()
	build, err := exec.CommandContext(ctx, "uname", "-srv").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("os=%s build=%s arch=%s go=%s commit=%s", runtime.GOOS, strings.TrimSpace(string(build)), runtime.GOARCH, runtime.Version(), os.Getenv("MEMORY_CAPTURE_TEST_COMMIT"))
}

func TestTransactionDiagnostics(t *testing.T) {
	for _, tt := range []struct{ stage, operation string }{
		{"lock-open", "lock open"},
		{"lock-flock", "lock flock"},
		{"temp-create", "temporary create"},
		{"write", "temporary write"},
		{"file-sync", "temporary sync"},
		{"close", "temporary close"},
		{"rename", "record rename"},
		{"dir-sync", "directory sync"},
	} {
		t.Run(tt.stage, func(t *testing.T) {
			home := t.TempDir()
			s := testStore(t, home)
			s.hook = func(stage string) error {
				if stage == tt.stage {
					return unix.ENOENT
				}
				return nil
			}
			p := testPayload()
			p.Messages[0].Text = "private-payload-must-not-enter-errors"
			b, err := s.Publish(context.Background(), testID, p)
			if b != nil || !errors.Is(err, unix.ENOENT) {
				t.Fatalf("failure executable or errno lost: %#v %v", b, err)
			}
			var errno unix.Errno
			if !errors.As(err, &errno) || errno != unix.ENOENT || !strings.Contains(err.Error(), tt.operation) || strings.Contains(err.Error(), p.Messages[0].Text) {
				t.Fatalf("missing operation/errno or exposed payload: %v", err)
			}
			got := testPending(t, testStore(t, home), testID)
			if tt.stage == "dir-sync" {
				if got == nil || !reflect.DeepEqual(got.Payload, p) || len(got.ChangedPaths) != 0 {
					t.Fatalf("incomplete surviving record: %#v", got)
				}
			} else if got != nil {
				t.Fatalf("pre-rename failure published: %#v", got)
			}
		})
	}
}

func TestRecordOpenDiagnostic(t *testing.T) {
	home := t.TempDir()
	s := testStore(t, home)
	ctx := context.Background()
	if _, err := s.Publish(ctx, testID, testPayload()); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(home, ".pyry/memory/capture", recordName(testID))
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing-private-target", name); err != nil {
		t.Fatal(err)
	}
	b, err := s.Pending(ctx, testID)
	if b != nil || !errors.Is(err, unix.ELOOP) || !strings.Contains(err.Error(), "record open") || !strings.Contains(err.Error(), "openat") {
		t.Fatalf("record failure lost operation/errno: %#v %v", b, err)
	}
	if target, e := os.Readlink(name); e != nil || target != "missing-private-target" {
		t.Fatalf("substitution changed: %q %v", target, e)
	}
}

const testProcessOutputLimit = 16 << 10

type testProcessOutput struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	truncated bool
}

func (o *testProcessOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := len(p)
	remaining := testProcessOutputLimit - o.buffer.Len()
	if n > remaining {
		p = p[:remaining]
		o.truncated = true
	}
	_, _ = o.buffer.Write(p) // bytes.Buffer.Write cannot fail.
	return n, nil
}

func (o *testProcessOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	result := o.buffer.String()
	if o.truncated {
		result += "\n[helper output truncated]"
	}
	return result
}

func testProcessCommand(ctx context.Context, home, note string) (*exec.Cmd, *testProcessOutput) {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "MEMORY_CAPTURE_TEST_HOME="+home, "MEMORY_CAPTURE_TEST_NOTE="+note)
	output := new(testProcessOutput)
	cmd.Stdout, cmd.Stderr = output, output
	return cmd, output
}

func TestProcessDiagnostics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd, output := testProcessCommand(ctx, t.TempDir(), "../rejected-note")
	if err := cmd.Run(); err == nil || !strings.Contains(output.String(), "phase=report") || !strings.Contains(output.String(), "invalid capture input") {
		t.Fatalf("missing child failure evidence: %v\n%s", err, output.String())
	}
}

func TestProcessOutputBound(t *testing.T) {
	o := new(testProcessOutput)
	p := bytes.Repeat([]byte("x"), testProcessOutputLimit+1)
	for i := 0; i < 2; i++ {
		if n, err := o.Write(p); n != len(p) || err != nil {
			t.Fatalf("output writer: %d %v", n, err)
		}
	}
	if o.buffer.Len() != testProcessOutputLimit || !strings.HasSuffix(o.String(), "\n[helper output truncated]") {
		t.Fatal("helper output is unbounded or truncation is unmarked")
	}
}
