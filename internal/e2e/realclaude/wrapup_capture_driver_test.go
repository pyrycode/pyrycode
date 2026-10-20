//go:build e2e_realclaude

package realclaude

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func wrapUpCaptureCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

// prepareWrapUpCaptureSource pins committed source independently of fixtures
// written by other live tests. The ordinary capture stamp checks still apply.
func prepareWrapUpCaptureSource(ctx context.Context, source, destination, revision string) error {
	fail := errors.New("cannot pin native capture source")
	if len(revision) != 40 || strings.Trim(revision, "0123456789abcdef") != "" {
		return fail
	}
	if wrapUpCaptureCommand(ctx, "git", "clone", "--quiet", "--shared", "--no-hardlinks", "--no-checkout", "--", source, destination).Run() != nil {
		return fail
	}
	if wrapUpCaptureCommand(ctx, "git", "-C", destination, "switch", "--quiet", "--detach", revision).Run() != nil {
		return fail
	}
	raw, err := wrapUpCaptureCommand(ctx, "git", "-C", destination, "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(raw)) != revision {
		return fail
	}
	raw, err = wrapUpCaptureCommand(ctx, "git", "-C", destination, "status", "--porcelain").Output()
	if err != nil || len(raw) != 0 {
		return fail
	}
	return nil
}

// wrapUpCaptureDriverEvidence retains only counts and closed failure classes.
// Child output is inspected transiently and cannot authorize capture completion.
type wrapUpCaptureDriverEvidence struct {
	Executed, Passed, Failed, Skipped int
	Failure                           string
}

func (d *wrapUpCaptureDriverEvidence) observe(raw []byte) {
	var event struct{ Action, Test, Output string }
	if json.Unmarshal(raw, &event) != nil {
		d.Failure = "stream"
		return
	}
	if event.Test != "TestRecordedClaude/clear-wrapup-skipped" && event.Test != "TestRecordedClaude" {
		return
	}
	if d.Failure == "none" || d.Failure == "unknown" {
		for _, phase := range []struct{ text, class string }{
			{"cannot compile original pool test assertions", "helper_build"},
			{"commit source before daemon suite capture", "source_dirty"},
			{"original test binary source changed", "source_revision"},
			{"daemon does not match capture source", "daemon_source"},
			{"original daemon assertions failed", "root"},
			{"lifecycle role evidence changed during original cleanup", "audit"},
		} {
			if strings.Contains(event.Output, phase.text) {
				d.Failure = phase.class
				break
			}
		}
	}
	if (event.Action == "fail" || event.Action == "skip") && d.Failure == "none" {
		d.Failure = "unknown"
	}
	if event.Test == "TestRecordedClaude/clear-wrapup-skipped" {
		var count *int
		switch event.Action {
		case "run":
			count = &d.Executed
		case "pass":
			count = &d.Passed
		case "fail":
			count = &d.Failed
		case "skip":
			count = &d.Skipped
		}
		if count != nil && *count < 2 {
			*count++
		}
	}
}

func (d wrapUpCaptureDriverEvidence) valid() bool {
	return d.Executed == 1 && d.Passed == 1 && d.Failed == 0 && d.Skipped == 0 && d.Failure == "none"
}
