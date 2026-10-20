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
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
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
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
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
		bare := regexp.MustCompile(`^[/a-zA-Z0-9._-]+$`).MatchString(path)
		double := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", `\`+"`").Replace(path) + `"`
		return bare && command == path || command == physicalQuote(path) || command == double
	case "composer-background":
		return regexp.MustCompile(`^[/a-zA-Z0-9._-]+$`).MatchString(filepath.Join(work, "composer-background.fifo")) && command == "cat "+filepath.Join(work, "composer-background.fifo")
	case "composer-foreground":
		expected := fmt.Sprintf(`printf '%%s\n' "$$" > %s; while [ ! -f %s ]; do sleep 0.1; done`, physicalQuote(filepath.Join(work, "foreground-started")), physicalQuote(filepath.Join(work, "foreground-released")))
		return command == expected
	}
	return false
}

// OwnedPhysicalOperation owns only the current replay's exec.Cmd group. Native
// task/tool IDs can select this capability later; they never become OS PIDs.
type OwnedPhysicalOperation struct {
	cmd          *exec.Cmd
	guard        *exec.Cmd
	guardPipe    *os.File
	home         string
	mu           sync.Mutex
	retired      bool
	done         chan struct{}
	watchdogDone chan struct{}
	waitErr      error
	status       FallbackWorkerStatus
}

// PhysicalInputCommand validates a closed command without reading or creating
// rig files. This is input-shape proof only; actual replay execution must use
// StartPhysicalOperation so current script/FIFO/mode/witness checks still run.
func PhysicalInputCommand(home string, s PhysicalOperationSpec, input json.RawMessage) (string, error) {
	if !filepath.IsAbs(home) || filepath.Clean(home) != home || s.Validate() != nil {
		return "", os.ErrInvalid
	}
	if !uniqueSuiteKeys(input) || safeCLIJSON(input) != nil {
		return "", os.ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(input, &fields) != nil {
		return "", os.ErrInvalid
	}
	for key := range fields {
		if key != "command" && key != "run_in_background" && key != "description" && key != "timeout" {
			return "", errors.New("unknown physical tool input")
		}
	}
	var command string
	if json.Unmarshal(fields["command"], &command) != nil || !s.CommandMatches(home, command) {
		return "", errors.New("foreign physical command")
	}
	background := false
	if raw, ok := fields["run_in_background"]; ok {
		if !bytes.Equal(bytes.TrimSpace(raw), []byte("true")) && !bytes.Equal(bytes.TrimSpace(raw), []byte("false")) {
			return "", os.ErrInvalid
		}
		if json.Unmarshal(raw, &background) != nil {
			return "", os.ErrInvalid
		}
	}
	if background != (s.Kind != "composer-foreground") {
		return "", errors.New("physical task posture changed")
	}
	if raw, ok := fields["description"]; ok {
		var text string
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &text) != nil || len(text) > 4096 {
			return "", os.ErrInvalid
		}
	}
	if raw, ok := fields["timeout"]; ok {
		var timeout int
		if json.Unmarshal(raw, &timeout) != nil || timeout < 1 || timeout > 240000 {
			return "", os.ErrInvalid
		}
	}
	return command, nil
}

func StartPhysicalOperation(home string, s PhysicalOperationSpec, input json.RawMessage) (*OwnedPhysicalOperation, error) {
	if s.CheckCurrentInput(home) != nil {
		return nil, errors.New("physical operation inputs unproved")
	}
	command, err := PhysicalInputCommand(home, s, input)
	if err != nil {
		return nil, err
	}
	// A separate unreaped group leader pins the PGID until every cancellation
	// decision and group drain is complete. The operation's actual Wait may reap
	// its own PID without transferring this group capability to a reused PID.
	guardR, guardW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	guard := exec.Command("sh", "-c", "read token <&3")
	guard.ExtraFiles = []*os.File{guardR}
	guard.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := guard.Start(); err != nil {
		guardR.Close()
		guardW.Close()
		return nil, err
	}
	guardR.Close()
	cmd := exec.Command("bash", "--noprofile", "--norc", "-c", command)
	cmd.Dir = filepath.Join(home, "work")
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "LANG=C"}
	cmd.Stdin = bytes.NewReader(nil)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: guard.Process.Pid}
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		_ = syscall.Kill(-guard.Process.Pid, syscall.SIGKILL)
		_ = guard.Wait()
		guardW.Close()
		return nil, err
	}
	op := &OwnedPhysicalOperation{cmd: cmd, guard: guard, guardPipe: guardW, home: home, done: make(chan struct{}), watchdogDone: make(chan struct{})}
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
	go func() {
		timer := time.NewTimer(3 * time.Minute)
		defer timer.Stop()
		select {
		case <-op.done:
		case <-op.watchdogDone:
		case <-timer.C:
			_ = op.signalGroup()
		}
	}()
	return op, nil
}
func (o *OwnedPhysicalOperation) signalGroup() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.retired {
		return errors.New("physical group capability retired")
	}
	// guard.Wait is called only under this lock after retirement. Even a guard
	// that died unexpectedly remains unreaped here, so this PID cannot be reused.
	err := syscall.Kill(-o.guard.Process.Pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}
func (o *OwnedPhysicalOperation) Running() bool {
	select {
	case <-o.done:
		return false
	default:
		return o.cmd.Process.Signal(syscall.Signal(0)) == nil
	}
}
func (o *OwnedPhysicalOperation) Wait(budget time.Duration) (FallbackWorkerStatus, error) {
	return o.WaitContext(context.Background(), budget)
}
func (o *OwnedPhysicalOperation) WaitContext(ctx context.Context, budget time.Duration) (FallbackWorkerStatus, error) {
	if ctx == nil || ctx.Err() != nil {
		return FallbackWorkerStatus{}, errors.New("physical operation wait aborted")
	}
	select {
	case <-ctx.Done():
		return FallbackWorkerStatus{}, errors.New("physical operation wait aborted")
	case <-o.done:
		if o.cmd.ProcessState == nil {
			return FallbackWorkerStatus{}, errors.New("missing actual physical Wait")
		}
		var exit *exec.ExitError
		if o.waitErr != nil && !errors.As(o.waitErr, &exit) {
			return o.status, errors.New("physical Wait failed without an exit status")
		}
		return o.status, nil
	case <-time.After(budget):
		return FallbackWorkerStatus{}, errors.New("physical operation completion deadline")
	}
}
func (o *OwnedPhysicalOperation) Stop() (FallbackWorkerStatus, error) {
	return o.StopContext(context.Background())
}
func (o *OwnedPhysicalOperation) StopContext(ctx context.Context) (FallbackWorkerStatus, error) {
	if ctx == nil || ctx.Err() != nil {
		return FallbackWorkerStatus{}, errors.New("physical operation stop aborted")
	}
	if !o.Running() {
		return FallbackWorkerStatus{}, errors.New("physical operation was not held at stop")
	}
	if err := o.signalGroup(); err != nil {
		return FallbackWorkerStatus{}, err
	}
	status, err := o.WaitContext(ctx, 3*time.Second)
	if err != nil || status != (FallbackWorkerStatus{ExitCode: -1, Signal: 9}) {
		return status, errors.New("physical stop lacks actual SIGKILL Wait")
	}
	return status, nil
}
func physicalGroupAlive(group int) (bool, error) {
	raw, err := exec.Command("ps", "-axo", "pgid=,stat=").Output()
	if err != nil {
		return false, errors.New("physical group inventory unavailable")
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return false, errors.New("physical group inventory malformed")
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			return false, err
		}
		if pid == group && !strings.HasPrefix(fields[1], "Z") {
			return true, nil
		}
	}
	return false, nil
}
func (o *OwnedPhysicalOperation) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.retired {
		return nil
	}
	if err := syscall.Kill(-o.guard.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		// Darwin reports EPERM for a pinned group containing only zombies. As in
		// test admission, accept this only after an actual readable group census
		// proves that no live member remains; a live/unknown group stays a failure.
		live, censusErr := physicalGroupAlive(o.guard.Process.Pid)
		if runtime.GOOS != "darwin" || err != syscall.EPERM || censusErr != nil || live {
			return err
		}
	}
	if _, err := o.Wait(3 * time.Second); err != nil {
		return err
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		live, err := physicalGroupAlive(o.guard.Process.Pid)
		if err != nil {
			return err
		}
		if !live {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("physical group did not drain while leader remained pinned")
		}
		time.Sleep(time.Millisecond)
	}
	// No future signal, including a delayed watchdog, may use this PGID once its
	// leader is reaped. Retire under the same lock first, then perform actual Wait.
	o.retired = true
	close(o.watchdogDone)
	o.guardPipe.Close()
	err := o.guard.Wait()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return errors.New("physical group guard Wait failed")
	}
	return nil
}
func (o *OwnedPhysicalOperation) CurrentPID() int { return o.cmd.Process.Pid }
func (o *OwnedPhysicalOperation) RequireForegroundWitness(home string) error {
	if home != o.home {
		return errors.New("foreground home rebound")
	}
	if _, err := os.Lstat(filepath.Join(home, "work", "foreground-released")); !os.IsNotExist(err) {
		return errors.New("foreground gate is released or unreadable")
	}
	path := filepath.Join(home, "work", "foreground-started")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64 {
		return errors.New("invalid foreground witness")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return errors.New("foreground witness open failed")
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) {
		return errors.New("foreground witness replaced")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 65))
	if err != nil || len(raw) > 64 {
		return errors.New("foreground witness incomplete")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid != o.CurrentPID() || !o.Running() {
		return errors.New("foreground witness does not belong to current shell")
	}
	return nil
}
