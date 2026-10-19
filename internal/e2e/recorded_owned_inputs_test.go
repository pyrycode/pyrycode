//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
)

func TestRecordedOwnedInputContract(t *testing.T) {
	for _, name := range []string{"same", "skill-body", "cwd", "missing", "oversized", "old-profile", "hook-same", "hook-script", "hook-settings", "hook-mode"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, "work")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestRecordedOwnedInputHelperProcess$", "-test.v")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PYRY_OWNED_INPUT_CONTROL="+name, "PYRY_OWNED_INPUT_HOME="+home)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("owned input control failed: %v %s", err, out)
			}
		})
	}
}
func TestRecordedOwnedInputHelperProcess(t *testing.T) {
	name := os.Getenv("PYRY_OWNED_INPUT_CONTROL")
	if name == "" {
		return
	}
	home := os.Getenv("PYRY_OWNED_INPUT_HOME")
	scenario.WriteProbeSkill(t, home)
	settings := filepath.Join(home, "settings.json")
	prompt := filepath.Join(home, "system.txt")
	if err := os.WriteFile(settings, []byte(`{"test":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prompt, []byte("system prompt"), 0600); err != nil {
		t.Fatal(err)
	}
	c := tapeProcessConfig{Home: home, Scenario: "skill-silence"}
	args := []string{"--settings", settings, "--append-system-prompt-file", prompt}
	if strings.HasPrefix(name, "hook-") {
		rig, err := scenario.WriteHookRig(filepath.Join(home, scenario.HookRigDir))
		if err != nil {
			t.Fatal(err)
		}
		if err = scenario.InstallHookUserSettings(home, rig); err != nil {
			t.Fatal(err)
		}
		c.Scenario = "hook-banner"
		before, err := tapeFiles(args, &c)
		if err != nil {
			t.Fatal(err)
		}
		if len(before) != 5 {
			t.Fatal("hook input inventory incomplete")
		}
		switch name {
		case "hook-script":
			if err = os.WriteFile(rig.ScriptPath, []byte(rig.Script+"#changed\n"), 0700); err != nil {
				t.Fatal(err)
			}
		case "hook-settings":
			if err = os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
		case "hook-mode":
			if err = os.Chmod(rig.ScriptPath, 0600); err != nil {
				t.Fatal(err)
			}
		}
		after, err := tapeFiles(args, &c)
		if err != nil {
			t.Fatal(err)
		}
		err = (&claudetape.Tape{Files: before}).CheckFiles(after)
		if (err == nil) != (name == "hook-same") {
			t.Fatal("hook exact input acceptance changed")
		}
		return
	}
	before, err := tapeFiles(args, &c)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 4 || len(before["$skill/pyry-probe"]) == 0 || len(before["$cwd"]) == 0 {
		t.Fatal("named skill/cwd inputs were not captured")
	}
	skill := filepath.Join(home, ".claude", "skills", scenario.SkillName, "SKILL.md")
	switch name {
	case "skill-body":
		raw, err := os.ReadFile(skill)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(skill, append(raw, []byte("additional instruction\n")...), 0600); err != nil {
			t.Fatal(err)
		}
	case "cwd":
		if err := os.Chdir(home); err != nil {
			t.Fatal(err)
		}
	case "missing":
		if err := os.Remove(skill); err != nil {
			t.Fatal(err)
		}
	case "oversized":
		if err := os.WriteFile(skill, []byte(strings.Repeat("x", claudetape.MaxBytes+1)), 0600); err != nil {
			t.Fatal(err)
		}
	case "old-profile":
		c.Scenario = "reply"
	}
	after, err := tapeFiles(args, &c)
	if name == "missing" || name == "oversized" {
		if err == nil {
			t.Fatal("invalid implicit input accepted")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if name == "old-profile" {
		if len(after) != 2 {
			t.Fatal("old profile file contract changed")
		}
		return
	}
	tape := claudetape.Tape{Files: before}
	if err := tape.CheckFiles(after); (err == nil) != (name == "same") {
		t.Fatal("owned input drift acceptance incorrect")
	}
}
