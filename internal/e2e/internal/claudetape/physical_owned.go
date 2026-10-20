package claudetape

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// PhysicalOperationSpec describes only an original test-owned closed operation.
// It carries no PID or result witness. Inspect requires the original rig to have
// already created the actual input; replay never restores a recorded script,
// FIFO, arrival marker, process ID or release file to satisfy an assertion.
// These primitives are not yet registered as a scenario or coverage claim.
type PhysicalOperationSpec struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"`
	Script  string `json:"script,omitempty"`
}

const physicalHome = "${CHILD_HOME}"

func physicalQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func physicalSpec(kind string) (PhysicalOperationSpec, error) {
	s := PhysicalOperationSpec{Version: 1, Kind: kind}
	switch kind {
	case "stop-hold":
		s.Script = "#!/bin/sh\numask 077\nprintf x >> " + physicalQuote(physicalHome+"/work/stop-hold.executed") + " || exit 1\nexec cat " + physicalQuote(physicalHome+"/work/stop-task.fifo") + "\n"
	case "composer-background", "composer-foreground":
	default:
		return s, errors.New("unknown physical operation")
	}
	return s, nil
}
func (s PhysicalOperationSpec) Validate() error {
	expected, err := physicalSpec(s.Kind)
	if err != nil || s != expected {
		return errors.New("foreign physical operation declaration")
	}
	return nil
}
func physicalWork(home string) (string, error) {
	if !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return "", os.ErrInvalid
	}
	// The test owns HOME and its work directory. Platform-owned ancestors may
	// include Darwin's /tmp or /var aliases; neither private directory may be a link.
	info, err := os.Lstat(home)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Mode()&os.ModeSymlink != 0 {
		return "", os.ErrInvalid
	}
	work := filepath.Join(home, "work")
	info, err = os.Lstat(work)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Mode()&os.ModeSymlink != 0 {
		return "", os.ErrInvalid
	}
	return work, nil
}
func physicalReadScript(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0700 || info.Size() > 4096 {
		return nil, os.ErrInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, os.ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(raw) > 4096 {
		return nil, os.ErrInvalid
	}
	return raw, nil
}
func InspectPhysicalOperation(home, kind string) (PhysicalOperationSpec, error) {
	s, err := physicalSpec(kind)
	if err != nil {
		return s, err
	}
	if err := s.CheckCurrentInput(home); err != nil {
		return s, err
	}
	return s, nil
}
func (s PhysicalOperationSpec) CheckCurrentInput(home string) error {
	if s.Validate() != nil {
		return os.ErrInvalid
	}
	work, err := physicalWork(home)
	if err != nil {
		return err
	}
	switch s.Kind {
	case "stop-hold":
		raw, err := physicalReadScript(filepath.Join(work, "stop-hold"))
		if err != nil || string(raw) != strings.ReplaceAll(s.Script, physicalHome, home) {
			return errors.New("current original hold script differs")
		}
		for _, name := range []string{"stop-hold.executed"} {
			if _, err := os.Lstat(filepath.Join(work, name)); !os.IsNotExist(err) {
				return errors.New("physical result exists before execution")
			}
		}
	case "composer-foreground":
		for _, name := range []string{"foreground-started", "foreground-released"} {
			if _, err := os.Lstat(filepath.Join(work, name)); !os.IsNotExist(err) {
				return errors.New("foreground result exists before execution")
			}
		}
		return nil
	}
	fifo := "stop-task.fifo"
	if s.Kind == "composer-background" {
		fifo = "composer-background.fifo"
	}
	info, err := os.Lstat(filepath.Join(work, fifo))
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 || info.Mode().Perm() != 0600 {
		return errors.New("missing original private FIFO")
	}
	return nil
}
func (s PhysicalOperationSpec) CommandMatches(home, command string) bool {
	work := filepath.Join(home, "work")
	switch s.Kind {
	case "stop-hold":
		path := filepath.Join(work, "stop-hold")
		command = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(command), ";"))
		command = strings.TrimSpace(strings.TrimPrefix(command, "exec "))
		// Match the original rig's closed executable-token grammar, never a
		// substring, argument, substitution or compound command.
		bare := !strings.ContainsAny(path, " \t\r\n'\"$`\\;&|<>()")
		double := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", `\`+"`").Replace(path) + `"`
		return bare && command == path || command == physicalQuote(path) || command == double
	case "composer-background":
		return command == "cat "+filepath.Join(work, "composer-background.fifo")
	case "composer-foreground":
		expected := fmt.Sprintf(`printf '%%s\n' "$$" > %s; while [ ! -f %s ]; do sleep 0.1; done`, physicalQuote(filepath.Join(work, "foreground-started")), physicalQuote(filepath.Join(work, "foreground-released")))
		return command == expected
	}
	return false
}

// OwnedPhysicalOperation owns only the current replay's exec.Cmd group. Native
// task/tool IDs can select this capability later; they never become OS PIDs.
type OwnedPhysicalOperation struct {
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	done    chan struct{}
	waitErr error
	status  FallbackWorkerStatus
}

func StartPhysicalOperation(home string, s PhysicalOperationSpec, input json.RawMessage) (*OwnedPhysicalOperation, error) {
	if s.CheckCurrentInput(home) != nil {
		return nil, errors.New("physical operation inputs unproved")
	}
	if !uniqueSuiteKeys(input) || safeCLIJSON(input) != nil {
		return nil, os.ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(input, &fields) != nil {
		return nil, os.ErrInvalid
	}
	for key := range fields {
		if key != "command" && key != "run_in_background" && key != "description" && key != "timeout" {
			return nil, errors.New("unknown physical tool input")
		}
	}
	var command string
	if json.Unmarshal(fields["command"], &command) != nil || !s.CommandMatches(home, command) {
		return nil, errors.New("foreign physical command")
	}
	background := false
	if raw, ok := fields["run_in_background"]; ok && json.Unmarshal(raw, &background) != nil {
		return nil, os.ErrInvalid
	}
	if background != (s.Kind != "composer-foreground") {
		return nil, errors.New("physical task posture changed")
	}
	if raw, ok := fields["description"]; ok {
		var text string
		if json.Unmarshal(raw, &text) != nil || len(text) > 4096 {
			return nil, os.ErrInvalid
		}
	}
	if raw, ok := fields["timeout"]; ok {
		var timeout int
		if json.Unmarshal(raw, &timeout) != nil || timeout < 1 || timeout > 240000 {
			return nil, os.ErrInvalid
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	cmd := exec.CommandContext(ctx, "bash", "--noprofile", "--norc", "-c", command)
	cmd.Dir = filepath.Join(home, "work")
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "LANG=C"}
	cmd.Stdin = bytes.NewReader(nil)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	op := &OwnedPhysicalOperation{cmd: cmd, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(op.done)
		op.waitErr = cmd.Wait()
		if cmd.ProcessState != nil {
			op.status.ExitCode = cmd.ProcessState.ExitCode()
			if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				op.status.Signal = int(status.Signal())
			}
		}
	}()
	return op, nil
}
func (o *OwnedPhysicalOperation) Running() bool {
	select {
	case <-o.done:
		return false
	default:
		return syscall.Kill(o.cmd.Process.Pid, 0) == nil
	}
}
func (o *OwnedPhysicalOperation) Wait(budget time.Duration) (FallbackWorkerStatus, error) {
	select {
	case <-o.done:
		if o.cmd.ProcessState == nil {
			return FallbackWorkerStatus{}, errors.New("missing actual physical Wait")
		}
		return o.status, nil
	case <-time.After(budget):
		return FallbackWorkerStatus{}, errors.New("physical operation completion deadline")
	}
}
func (o *OwnedPhysicalOperation) Stop() (FallbackWorkerStatus, error) {
	if !o.Running() {
		return FallbackWorkerStatus{}, errors.New("physical operation was not held at stop")
	}
	o.cancel()
	status, err := o.Wait(3 * time.Second)
	if err != nil || status != (FallbackWorkerStatus{ExitCode: -1, Signal: 9}) {
		return status, errors.New("physical stop lacks actual SIGKILL Wait")
	}
	return status, nil
}
func (o *OwnedPhysicalOperation) Close() error {
	defer o.cancel()
	select {
	case <-o.done:
		return nil
	default:
	}
	o.cancel()
	_, err := o.Wait(3 * time.Second)
	return err
}
func (o *OwnedPhysicalOperation) CurrentPID() int { return o.cmd.Process.Pid }
func (o *OwnedPhysicalOperation) RequireForegroundWitness(home string) error {
	raw, err := os.ReadFile(filepath.Join(home, "work", "foreground-started"))
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid != o.CurrentPID() || !o.Running() {
		return errors.New("foreground witness does not belong to current shell")
	}
	return nil
}
