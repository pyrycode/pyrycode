//go:build e2e_realclaude

package realclaude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

const wrapUpNativeFixture = "testdata/clear-wrapup-skipped-native.json"
const wrapUpNativeRootFixture = "testdata/clear-wrapup-skipped-native-root.json"
const wrapUpNativeSuiteFixture = "testdata/clear-wrapup-skipped-native-suite.json"

type wrapUpRootObservation struct {
	Outcome, DeadlineActivation, InternalWriteAttempt string
	RootPassed                                        bool
	Observation                                       *claudetape.WrapUpObservation
}

func (r wrapUpRootObservation) validate(broker *claudetape.WrapUpObservation) error {
	o := r.Observation
	if o == nil || o.Validate(false) != nil || !r.RootPassed || r.DeadlineActivation != "unknown" || r.InternalWriteAttempt != "unknown" || !slices.Contains([]string{"unknown", "deadline", "write_failed", "not_idle", "delivery_busy", "capture_busy", "readiness_failed", "terminal_failed"}, r.Outcome) || o.Revision != broker.Revision || o.SourceSHA256 != broker.SourceSHA256 || o.DaemonSHA256 != broker.DaemonSHA256 {
		return errors.New("invalid native root observation")
	}
	if len(o.Events) != 3 || o.Events[0].Stage != "reset_send" || o.Events[1].Stage != "reset_observed" || o.Events[2].Stage != "predecessor_gone" {
		return errors.New("incomplete native root observation")
	}
	return nil
}

func wrapUpEvidencePath(t *testing.T, name string) string {
	t.Helper()
	dir := os.Getenv("PYRY_CLAUDE_TAPE_EVIDENCE")
	info, err := os.Lstat(dir)
	if !filepath.IsAbs(dir) || err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("native observation requires a private durable evidence directory")
	}
	return filepath.Join(dir, name)
}

// captureSkippedWrapUp delegates only an unowned native invocation. The stamped
// lifecycle helper re-enters the same root with its existing recording ownership.
func captureSkippedWrapUp(t *testing.T) bool {
	t.Helper()
	if os.Getenv("PYRY_LIFECYCLE_RECORDING_CONFIG") != "" {
		return false
	}
	var observation claudetape.WrapUpObservation
	if _, err := os.Lstat(wrapUpNativeFixture); err == nil {
		if claudetape.ReadLifecycleControl(wrapUpNativeFixture, 16384, &observation) != nil || observation.Validate(true) != nil {
			t.Fatal("invalid reviewed native wrap-up observation")
		}
		var root wrapUpRootObservation
		if claudetape.ReadLifecycleControl(wrapUpNativeRootFixture, 16384, &root) != nil || root.validate(&observation) != nil {
			t.Fatal("missing or invalid reviewed native root observation")
		}
		_ = readWrapUpNativeSuite(t, wrapUpNativeSuiteFixture, &observation)
		return false
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cannot inspect native observation")
	}
	if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Fatal("native wrap-up capture requires dispatcher live credentials; no usable capture exists")
	}
	dir, err := os.MkdirTemp("", "pyry-3673-native-")
	if err != nil {
		t.Fatal("cannot create durable native evidence directory")
	}
	t.Logf("native wrap-up capture evidence: %s", dir)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	binary := filepath.Join(dir, "driver.test")
	compile := exec.CommandContext(ctx, "go", "test", "-c", "-tags=e2e", "-o", binary, ".")
	compile.Dir = ".."
	if err := compile.Run(); err != nil {
		t.Fatal("cannot compile native capture driver")
	}
	cmd := exec.CommandContext(ctx, "go", "tool", "test2json", "-t", binary, "-test.v", "-test.count=1", "-test.run=^TestRecordedClaude$/^clear-wrapup-skipped$", "-test.timeout=23m")
	cmd.Dir = ".."
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if key == "PYRY_CLAUDE_TAPE_MODE" || key == "PYRY_CLAUDE_TAPE_DIR" || key == "PYRY_CLAUDE_TAPE_EVIDENCE" || key == "PYRY_CLAUDE_TAPE_SCENARIOS" {
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, "PYRY_CLAUDE_TAPE_MODE=capture", "PYRY_CLAUDE_TAPE_DIR="+dir, "PYRY_CLAUDE_TAPE_EVIDENCE="+dir, `PYRY_CLAUDE_TAPE_SCENARIOS=["clear-wrapup-skipped"]`)
	stream, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal("cannot observe native driver counts")
	}
	if err := cmd.Start(); err != nil {
		t.Fatal("cannot start native capture driver")
	}
	counts := struct{ Executed, Passed, Failed, Skipped int }{}
	scan := bufio.NewScanner(stream)
	scan.Buffer(make([]byte, 4096), claudetape.MaxBytes)
	for scan.Scan() {
		var event struct{ Action, Test string }
		if json.Unmarshal(scan.Bytes(), &event) != nil {
			continue
		}
		if event.Test == "TestRecordedClaude/clear-wrapup-skipped" {
			switch event.Action {
			case "run":
				counts.Executed++
			case "pass":
				counts.Passed++
			case "fail":
				counts.Failed++
			case "skip":
				counts.Skipped++
			}
		}
	}
	err = cmd.Wait()
	if claudetape.PublishLifecycleControl(filepath.Join(dir, "driver-counts.json"), counts) != nil {
		t.Fatal("cannot retain native driver counts")
	}
	if err != nil || scan.Err() != nil || counts.Executed != 1 || counts.Passed != 1 || counts.Failed != 0 || counts.Skipped != 0 {
		t.Fatal("native wrap-up capture failed; durable evidence contains no accepted capture")
	}
	path := filepath.Join(dir, "clear-wrapup-skipped-bound-native.json")
	if claudetape.ReadLifecycleControl(path, 16384, &observation) != nil || observation.Validate(true) != nil {
		t.Fatal("native capture lacks complete broker observations")
	}
	var root wrapUpRootObservation
	if claudetape.ReadLifecycleControl(filepath.Join(dir, "clear-wrapup-skipped-root-native.json"), 16384, &root) != nil || root.validate(&observation) != nil {
		t.Fatal("native capture lacks root observations")
	}
	suite := readWrapUpNativeSuite(t, filepath.Join(dir, "clear-wrapup-skipped.json"), &observation)
	if suite.Save(wrapUpNativeSuiteFixture, claudetape.LifecycleProfiles()["clear-wrapup-skipped"].Roles) != nil || claudetape.PublishLifecycleControl(wrapUpNativeRootFixture, root) != nil {
		t.Fatal("cannot publish native capture candidates")
	}
	if err := observation.Save(wrapUpNativeFixture); err != nil {
		t.Fatal("cannot publish native observation candidate")
	}
	t.Logf("native root executed=1 passed=1 failed=0 skipped=0; review and recover %s before replay repair", dir)
	return true
}

func readWrapUpNativeSuite(t *testing.T, path string, observation *claudetape.WrapUpObservation) *claudetape.LifecycleSuite {
	t.Helper()
	var suite claudetape.LifecycleSuite
	if claudetape.ReadLifecycleControl(path, claudetape.MaxBytes, &suite) != nil || suite.Validate(claudetape.LifecycleProfiles()["clear-wrapup-skipped"].Roles) != nil || suite.Scenario != "clear-wrapup-skipped" || suite.Producer.Revision != observation.Revision || suite.Producer.BinarySHA256 != observation.SourceSHA256 || suite.DaemonSHA256 != observation.DaemonSHA256 {
		t.Fatal("missing or mismatched complete native suite")
	}
	return &suite
}

func TestWrapUpNativeRootObservation(t *testing.T) {
	o := claudetape.NewWrapUpObservation(strings.Repeat("a", 40), strings.Repeat("b", 64), strings.Repeat("c", 64))
	for _, stage := range []string{"reset_send", "reset_observed", "predecessor_gone"} {
		o.Add(stage, nil, nil)
	}
	valid := wrapUpRootObservation{"deadline", "unknown", "unknown", true, o}
	if valid.validate(o) != nil {
		t.Fatal("valid measured root rejected")
	}
	for _, tc := range []struct {
		name   string
		change func(*wrapUpRootObservation)
	}{
		{"failed-root", func(r *wrapUpRootObservation) { r.RootPassed = false }},
		{"unmeasured-activation", func(r *wrapUpRootObservation) { r.DeadlineActivation = "complete" }},
		{"arbitrary-outcome", func(r *wrapUpRootObservation) { r.Outcome = "private error" }},
		{"combined-outcomes", func(r *wrapUpRootObservation) { r.Outcome = "unknown|deadline" }},
		{"missing-record", func(r *wrapUpRootObservation) { r.Observation = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := valid
			tc.change(&r)
			if r.validate(o) == nil {
				t.Fatal("invalid root accepted")
			}
		})
	}
}

func beginWrapUpRootObservation(t *testing.T, home string, d *bootstrapDaemon) *claudetape.WrapUpObservation {
	t.Helper()
	c, ok := lifecycleRecordingCurrent(t)
	if !ok || c.Process.Mode != "capture" || c.Process.Scenario != "clear-wrapup-skipped" {
		return nil
	}
	path := wrapUpEvidencePath(t, "clear-wrapup-skipped-root-native.json")
	o := claudetape.NewWrapUpObservation(poolRecordedRevision, c.SourceBinarySHA256, c.DaemonSHA256)
	t.Cleanup(func() {
		var stopped claudetape.LifecycleStopObservation
		if claudetape.ReadLifecycleControl(filepath.Join(home, "recording-children", "bound.stopped"), 16384, &stopped) == nil && stopped.Kind == "not-running" && stopped.PID > 1 && syscall.Kill(stopped.PID, 0) == syscall.ESRCH {
			o.Add("predecessor_gone", nil, nil)
		}
		// Match only fixed daemon-authored event tokens. Internal activation and
		// attempted writes without a broker arrival remain unobserved.
		outcome := "unknown"
		for _, event := range []string{"deadline", "write_failed", "not_idle", "delivery_busy", "capture_busy", "readiness_failed", "terminal_failed"} {
			if strings.Contains(d.stderr.String(), "event=reset.wrapup."+event) {
				outcome = event
				break
			}
		}
		result := wrapUpRootObservation{outcome, "unknown", "unknown", !t.Failed(), o}
		if o.Validate(false) != nil || claudetape.PublishLifecycleControl(path, result) != nil {
			t.Error("cannot retain native reset observations")
		}
	})
	return o
}
