package apps

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func testCandidateDiagnostics(t *testing.T) context.Context {
	t.Helper()
	return context.WithValue(context.Background(), candidateDiagnosticKey{}, func(d candidateDiagnostic) {
		t.Logf("candidate command=%s pid=%d exit=%d wait=%v cleanup=%v cancellation=%v", d.Command, d.PID, d.ExitCode, d.Wait, d.Cleanup, d.Cancel)
	})
}

func TestCandidateCommandDiagnostics(t *testing.T) {
	for _, name := range []string{"success", "command", "cleanup", "both"} {
		t.Run(name, func(t *testing.T) {
			r, tools, c, config := testCandidate(t)
			failedCommand := name == "command" || name == "both"
			failedCleanup := name == "cleanup" || name == "both"
			if failedCommand {
				c.Fail = "run build"
				testCandidateConfig(t, config, c)
			}
			stage, removeStage, err := candidateStaging(context.Background(), filepath.Join(r.root, testApp, "source"))
			backupOK(t, err)
			defer removeStage()
			env, removeEnv, err := candidateEnvironment(stage, tools.Node)
			backupOK(t, err)
			defer removeEnv()
			var got []candidateDiagnostic
			ctx := context.WithValue(context.Background(), candidateDiagnosticKey{}, func(d candidateDiagnostic) { got = append(got, d) })
			cleanup := func(pid int) error {
				err := candidateCleanup(pid)
				if failedCleanup {
					return errors.Join(err, syscall.EPERM)
				}
				return err
			}
			_, err = candidateCommandWithCleanup(ctx, stage, env, tools.Node, []string{tools.NPM, "run", "build"}, false, cleanup)
			if len(got) != 1 {
				t.Fatalf("want one bounded observation: %+v", got)
			}
			d := got[0]
			wantExit := 0
			if failedCommand {
				wantExit = 4
			}
			var retirement *candidateRetirementError
			if d.Command != "build" || d.PID < 1 || d.ExitCode != wantExit || (d.Wait != nil) != failedCommand || errors.Is(d.Cleanup, syscall.EPERM) != failedCleanup || d.Cancel != nil || errors.As(err, &retirement) != failedCleanup {
				t.Fatalf("lost command/retirement distinction: %+v error=%v", d, err)
			}
		})
	}
}

func TestCandidateDiagnosticPublicBoundary(t *testing.T) {
	r, tools, c, config := testCandidate(t)
	c.Fail = "run build"
	testCandidateConfig(t, config, c)
	observed := false
	ctx := context.WithValue(context.Background(), candidateDiagnosticKey{}, func(d candidateDiagnostic) {
		if strings.Contains(d.Command, "SECRET") || strings.Contains(d.Command, "/") {
			t.Fatalf("unsafe command label %q", d.Command)
		}
		if d.Command == "build" {
			observed = d.ExitCode == 4 && d.Wait != nil && d.Cleanup == nil
		}
	})
	_, err := r.PrepareCandidate(ctx, testHost, testApp, tools)
	testCandidateFailure(t, err, "build")
	if !observed || errors.Unwrap(err) != nil {
		t.Fatalf("missing private evidence or public error unwrap: observed=%v error=%v", observed, err)
	}
}

func TestCandidateDiagnosticBeforeStart(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "start failure", true: "cancelled"}[cancelled], func(t *testing.T) {
			var got candidateDiagnostic
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), candidateDiagnosticKey{}, func(d candidateDiagnostic) { got = d }))
			defer cancel()
			if cancelled {
				cancel()
			}
			_, err := candidateCommandWithCleanup(ctx, t.TempDir(), nil, "/nonexistent/SECRET", []string{"SECRET"}, false, func(int) error {
				t.Fatal("cleanup ran without a started process")
				return nil
			})
			if err == nil || got.PID != 0 || got.ExitCode != -1 || got.Command != "service" || got.Cleanup != nil || (got.Wait != nil) == cancelled || errors.Is(got.Cancel, context.Canceled) != cancelled {
				t.Fatalf("wrong pre-start diagnostic: %+v error=%v", got, err)
			}
		})
	}
}
