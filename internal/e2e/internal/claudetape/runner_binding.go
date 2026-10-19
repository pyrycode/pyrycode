package claudetape

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type RunnerBinding struct {
	home, workdir, session string
	args                   []string
}

func NewRunnerBinding(scenario, role string, args []string, home, workdir string) (*RunnerBinding, error) {
	fail := errors.New("runner role arguments changed")
	p, ok := RunnerProfiles()[scenario]
	if !ok {
		return nil, fail
	}
	owned, ok := p.Roles[role]
	if !ok || !filepath.IsAbs(home) || workdir != filepath.Join(home, owned.Workdir) {
		return nil, fail
	}
	b := &RunnerBinding{home: home, workdir: workdir, session: owned.SessionID, args: append([]string(nil), args...)}
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if seen[flag] {
			return nil, fail
		}
		seen[flag] = true
		switch flag {
		case "--session-id":
			if i+1 == len(args) || args[i+1] != owned.SessionID {
				return nil, fail
			}
			b.args[i+1] = "${RUNNER_SESSION}"
			i++
		case "--input-format", "--output-format":
			if i+1 == len(args) || args[i+1] != "stream-json" {
				return nil, fail
			}
			i++
		case "--model", "--effort":
			if i+1 == len(args) || args[i+1] == "" {
				return nil, fail
			}
			i++
		case "--setting-sources":
			if scenario != "runner-applied-settings" || i+1 == len(args) || args[i+1] != "" {
				return nil, fail
			}
			i++
		case "--verbose", "--include-partial-messages", "--forward-subagent-text", "--replay-user-messages", "--dangerously-skip-permissions", "--prompt-suggestions":
		default:
			return nil, fail
		}
	}
	for _, flag := range []string{"--session-id", "--input-format", "--output-format", "--verbose", "--model", "--dangerously-skip-permissions"} {
		if !seen[flag] {
			return nil, fail
		}
	}
	if scenario == "runner-applied-settings" && (!seen["--setting-sources"] || seen["--effort"] != (role == "explicit")) {
		return nil, fail
	}
	return b, nil
}
func (b *RunnerBinding) Args() []string { return append([]string(nil), b.args...) }
func (b *RunnerBinding) Frame(raw []byte, restore bool) ([]byte, error) {
	v, err := object(raw)
	if err != nil {
		return nil, err
	}
	if id, exists := v["session_id"]; exists {
		want, got := any(b.session), "${RUNNER_SESSION}"
		if restore {
			want, got = "${RUNNER_SESSION}", b.session
		}
		if id != want {
			return nil, errors.New("runner session identity changed")
		}
		v["session_id"] = got
	}
	if v["type"] == "system" && v["subtype"] == "init" {
		if cwd, exists := v["cwd"]; exists {
			want, got := any(b.workdir), "${RUNNER_CWD}"
			if restore {
				want, got = "${RUNNER_CWD}", b.workdir
			}
			if cwd != want {
				return nil, errors.New("runner cwd changed")
			}
			v["cwd"] = got
		}
	}
	var paths func(any) any
	paths = func(value any) any {
		switch x := value.(type) {
		case string:
			if restore {
				return strings.ReplaceAll(x, "${RUNNER_HOME}", b.home)
			}
			return strings.ReplaceAll(x, b.home, "${RUNNER_HOME}")
		case map[string]any:
			for k, v := range x {
				x[k] = paths(v)
			}
		case []any:
			for i, v := range x {
				x[i] = paths(v)
			}
		}
		return value
	}
	return json.Marshal(paths(v))
}

// These original profiles launch in empty controlled workdirs without input
// files. Applied-settings additionally disables saved setting sources on argv;
// provider-created HOME state between its arms remains a current-provider duty.
func (b *RunnerBinding) Files() (map[string]json.RawMessage, error) {
	info, err := os.Lstat(b.workdir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, errors.New("runner workdir changed")
	}
	entries, err := os.ReadDir(b.workdir)
	if err != nil || len(entries) != 0 {
		return nil, errors.New("runner workdir is not empty")
	}
	rel, err := filepath.Rel(b.home, b.workdir)
	if err != nil {
		return nil, err
	}
	cwd, _ := json.Marshal(rel)
	return map[string]json.RawMessage{"$cwd": cwd, "$workdir-mode": json.RawMessage(`448`), "$input-policy": json.RawMessage(`"fresh-empty-workdir/no-file-argv"`)}, nil
}
func RunRunnerProcess(c PoolProcessConfig, role string, args []string, input *os.File, output, diagnostic io.Writer) (int, error) {
	cwd, err := os.Getwd()
	if err != nil || cwd != c.WorkDir || os.Getenv("HOME") != c.Home || (c.Mode != "capture" && c.Mode != "replay") {
		return 0, errors.New("runner process environment changed")
	}
	b, err := NewRunnerBinding(c.Scenario, role, args, c.Home, c.WorkDir)
	if err != nil {
		return 0, err
	}
	files, err := b.Files()
	if err != nil {
		return 0, err
	}
	c.Scenario += "-" + role
	return runBoundProcess(c, args, b, files, input, output, diagnostic)
}

func validateRunnerSavedInputs(scenario, role string, tape *Tape) error {
	fail := errors.New("runner saved argument or input inventory changed")
	spec, ok := RunnerProfiles()[scenario].Roles[role]
	if !ok {
		return fail
	}
	args := append([]string(nil), tape.Args...)
	for i, arg := range args {
		if arg == "--session-id" {
			if i+1 == len(args) || args[i+1] != "${RUNNER_SESSION}" {
				return fail
			}
			args[i+1] = spec.SessionID
		}
	}
	home := "/tmp/runner-argument-validation"
	b, err := NewRunnerBinding(scenario, role, args, home, filepath.Join(home, spec.Workdir))
	if err != nil {
		return fail
	}
	a, _ := json.Marshal(b.Args())
	saved, _ := json.Marshal(tape.Args)
	if string(a) != string(saved) {
		return fail
	}
	cwd, _ := json.Marshal(spec.Workdir)
	expected := map[string]json.RawMessage{"$cwd": cwd, "$workdir-mode": json.RawMessage(`448`), "$input-policy": json.RawMessage(`"fresh-empty-workdir/no-file-argv"`)}
	if (&Tape{Files: expected}).CheckFiles(tape.Files) != nil {
		return fail
	}
	return nil
}
