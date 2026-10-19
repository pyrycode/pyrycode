//go:build e2e

package e2e

import (
	"context"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestRecordedScenarioSelectionControls(t *testing.T) {
	for _, name := range []string{"all", "subset", "empty", "unknown", "duplicate", "malformed", "unlisted-driver", "missing-driver"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRecordedSelectionHelperProcess$", "-test.v")
			cmd.Env = append(os.Environ(), "PYRY_RECORDED_SELECTION_CONTROL="+name)
			out, err := cmd.CombinedOutput()
			valid := name == "all" || name == "subset"
			if ctx.Err() != nil || (err == nil) != valid {
				t.Fatalf("selection acceptance err=%v output=%s", err, out)
			}
			if valid && !strings.Contains(string(out), "selected cases validated") {
				t.Fatal("helper did not execute selection")
			}
		})
	}
}

func TestRecordedSelectionHelperProcess(t *testing.T) {
	name := os.Getenv("PYRY_RECORDED_SELECTION_CONTROL")
	if name == "" {
		return
	}
	raw := map[string]string{"subset": `["reply","permission-deny"]`, "empty": `[]`, "unknown": `["not-a-scenario"]`, "duplicate": `["reply","reply"]`, "malformed": `["reply"`}[name]
	if raw != "" {
		t.Setenv("PYRY_CLAUDE_TAPE_SCENARIOS", raw)
	} else {
		if err := os.Unsetenv("PYRY_CLAUDE_TAPE_SCENARIOS"); err != nil {
			t.Fatal(err)
		}
	}
	if name == "unlisted-driver" {
		recordedScenarioDrivers["unlisted"] = runTapeReply
	}
	if name == "missing-driver" {
		delete(recordedScenarioDrivers, "reply")
	}
	selected := selectedRecordedScenarios(t)
	if name == "all" && len(selected) != len(recordedScenarioDrivers)+len(recordedPoolProfiles)+len(claudetape.DaemonSuiteProfiles())+len(claudetape.CLIProfiles())+len(claudetape.RunnerProfiles())+1 {
		t.Fatal("default selection omitted a registered case")
	}
	if name == "subset" && strings.Join(selected, ",") != "reply,permission-deny" {
		t.Fatal("named subset was changed")
	}
	t.Log("selected cases validated")
}
